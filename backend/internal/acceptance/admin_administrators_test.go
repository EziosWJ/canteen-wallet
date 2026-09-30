// Acceptance coverage for CW-28 (#32): a signed-in administrator creates further
// administrators. Assertions observe HTTP status, the account list and the audit
// trail; passwords and hashes must never surface.
package acceptance

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// administratorSummary mirrors one entry of the administrator list.
type administratorSummary struct {
	ID                int64  `json:"id"`
	Username          string `json:"username"`
	SecondFactorBound bool   `json:"second_factor_bound"`
	CreatedAt         string `json:"created_at"`
}

func (e *env) administrators(token string) []administratorSummary {
	e.t.Helper()
	var page struct {
		Items []administratorSummary `json:"administrators"`
	}
	e.get(e.public.URL, "/api/admin/administrators", token).expect(e.t, http.StatusOK, &page)
	return page.Items
}

func (e *env) createAdministrator(token, username, password string) apiResponse {
	e.t.Helper()
	return e.post(e.public.URL, "/api/admin/administrators", token, map[string]string{
		"username": username, "password": password,
	})
}

// TestAdministratorCanCreateAColleagueWhoThenSignsIn covers the happy path,
// including that the new account needs no second factor to start.
func TestAdministratorCanCreateAColleagueWhoThenSignsIn(t *testing.T) {
	e := newEnv(t)
	var created struct {
		Administrator administratorSummary `json:"administrator"`
	}
	e.createAdministrator(e.adminToken, "colleague-admin", "colleague-admin-password").
		expect(t, http.StatusCreated, &created)
	if created.Administrator.Username != "colleague-admin" || created.Administrator.SecondFactorBound {
		t.Fatalf("unexpected created administrator: %+v", created.Administrator)
	}
	// The new administrator signs in with the password alone.
	session := e.loginAdmin("colleague-admin", "colleague-admin-password", "")
	if len(e.administrators(session)) != 2 {
		t.Fatalf("the new administrator sees %d accounts, want 2", len(e.administrators(session)))
	}
}

// TestNewAdministratorCannotBeCreatedWithAnExistingName covers the conflict, and
// that nothing extra is created.
func TestNewAdministratorCannotBeCreatedWithAnExistingName(t *testing.T) {
	e := newEnv(t)
	e.createAdministrator(e.adminToken, adminUsername, "another-admin-password").
		expect(t, http.StatusConflict, nil)
	if items := e.administrators(e.adminToken); len(items) != 1 {
		t.Fatalf("a refused duplicate left %d accounts", len(items))
	}
}

// TestNewAdministratorRejectsInvalidInputAndCreatesNothing covers validation.
func TestNewAdministratorRejectsInvalidInputAndCreatesNothing(t *testing.T) {
	e := newEnv(t)
	for name, payload := range map[string]map[string]string{
		"empty username": {"username": "", "password": "another-admin-password"},
		"short username": {"username": "ab", "password": "another-admin-password"},
		"illegal chars":  {"username": "Not Allowed!", "password": "another-admin-password"},
		"short password": {"username": "valid-name", "password": "short"},
		"empty password": {"username": "valid-name", "password": ""},
	} {
		e.post(e.public.URL, "/api/admin/administrators", e.adminToken, payload).
			expect(t, http.StatusBadRequest, nil)
		_ = name
	}
	if items := e.administrators(e.adminToken); len(items) != 1 {
		t.Fatalf("refused input left %d accounts", len(items))
	}
}

// TestUnauthenticatedRequestCannotCreateAnAdministrator covers the entry being
// closed to anyone without a session.
func TestUnauthenticatedRequestCannotCreateAnAdministrator(t *testing.T) {
	e := newEnv(t)
	payload := map[string]string{"username": "sneaky-admin", "password": "sneaky-admin-password"}
	e.post(e.public.URL, "/api/admin/administrators", "", payload).expect(t, http.StatusUnauthorized, nil)
	e.post(e.public.URL, "/api/admin/administrators", "not-a-real-token", payload).expect(t, http.StatusUnauthorized, nil)
	if items := e.administrators(e.adminToken); len(items) != 1 {
		t.Fatalf("an unauthenticated request left %d accounts", len(items))
	}
}

// TestNewAdministratorIsAuditedWithoutSecrets covers the audit requirement.
func TestNewAdministratorIsAuditedWithoutSecrets(t *testing.T) {
	e := newEnv(t)
	const password = "colleague-admin-password"
	e.createAdministrator(e.adminToken, "colleague-admin", password).expect(t, http.StatusCreated, nil)
	items := e.auditEvents(e.adminToken)
	found := false
	for _, item := range items {
		if item.Action != "ADMIN_ACCOUNT_CREATED" {
			continue
		}
		found = true
		if strings.Contains(item.Details, password) || strings.Contains(item.Details, "$argon2") {
			t.Fatalf("audit leaked credential material: %s", item.Details)
		}
	}
	if !found {
		t.Fatal("creating an administrator was not audited")
	}
}

// TestEveryAdministratorHasEqualAuthority covers the "no role tiers" rule: the
// colleague can create administrators and change the consumption modes too.
func TestEveryAdministratorHasEqualAuthority(t *testing.T) {
	e := newEnv(t)
	e.createAdministrator(e.adminToken, "colleague-admin", "colleague-admin-password").
		expect(t, http.StatusCreated, nil)
	colleague := e.loginAdmin("colleague-admin", "colleague-admin-password", "")
	e.createAdministrator(colleague, "third-admin", "third-admin-password").expect(t, http.StatusCreated, nil)
	e.setModes(colleague, true, false).expect(t, http.StatusOK, nil)
	e.expectModes(t, colleague, true, false)
}

// TestCreatedAdministratorStartsUnboundAndBindsItself covers the hand-off from
// CW-28 to CW-29: creation leaves no binding, and the colleague adopts one.
func TestCreatedAdministratorStartsUnboundAndBindsItself(t *testing.T) {
	e := newEnv(t)
	e.createAdministrator(e.adminToken, "colleague-admin", "colleague-admin-password").
		expect(t, http.StatusCreated, nil)
	colleague := e.loginAdmin("colleague-admin", "colleague-admin-password", "")
	if state := e.security(colleague); state.SecondFactorBound {
		t.Fatalf("a new administrator must start unbound: %+v", state)
	}
	enrollment := e.startEnrollment(colleague)
	secret := e.pendingSecret(enrollment.Secret)
	e.post(e.public.URL, "/api/admin/security/enrollment/confirm", colleague,
		map[string]string{"second_factor_code": totpCode(secret, time.Now())}).expect(t, http.StatusOK, nil)
	// The colleague's other session was revoked; the new factor is now required.
	e.loginAdminRaw("colleague-admin", "colleague-admin-password", "").expect(t, http.StatusUnauthorized, nil)
	e.loginAdmin("colleague-admin", "colleague-admin-password", totpCode(secret, time.Now().Add(30*time.Second)))
}
