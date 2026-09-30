// Acceptance coverage for CW-25 (#29): the administrator second factor is an
// optional binding. Assertions stay at the HTTP boundary and on the audit trail
// the admin API exposes.
package acceptance

import (
	"crypto/rand"
	"encoding/base32"
	"net/http"
	"strings"
	"testing"
	"time"
)

type securityState struct {
	SecondFactorBound bool `json:"second_factor_bound"`
	EnrollmentPending bool `json:"enrollment_pending"`
	EnrollmentExpires any  `json:"enrollment_expires_at"`
}

type enrollmentResponse struct {
	OTPAuthURI string    `json:"otpauth_uri"`
	Secret     string    `json:"secret"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// pendingSecret decodes the base32 secret an enrollment response returns.
func (e *env) pendingSecret(encoded string) []byte {
	e.t.Helper()
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(encoded)
	if err != nil || len(secret) != 20 {
		e.t.Fatalf("decode pending secret %q: %v", encoded, err)
	}
	return secret
}

func (e *env) security(token string) securityState {
	e.t.Helper()
	var state securityState
	e.get(e.public.URL, "/api/admin/security", token).expect(e.t, http.StatusOK, &state)
	return state
}

// startEnrollment opens a pending binding for the given session.
func (e *env) startEnrollment(token string) enrollmentResponse {
	e.t.Helper()
	var enrollment enrollmentResponse
	e.post(e.public.URL, "/api/admin/security/enrollment", token, map[string]any{}).
		expect(e.t, http.StatusOK, &enrollment)
	if !strings.HasPrefix(enrollment.OTPAuthURI, "otpauth://totp/") || enrollment.Secret == "" {
		e.t.Fatalf("enrollment did not return a scannable URI: %+v", enrollment)
	}
	return enrollment
}

// setExistingBinding writes an authenticator secret directly, standing in for a
// database that was already bound before this release.
func (e *env) setExistingBinding(username string) []byte {
	e.t.Helper()
	var id int64
	if err := e.db.QueryRowContext(e.t.Context(), `SELECT id FROM administrators WHERE username=?`, username).Scan(&id); err != nil {
		e.t.Fatalf("find administrator: %v", err)
	}
	secret := make([]byte, 20)
	if _, err := rand.Read(secret); err != nil {
		e.t.Fatalf("generate secret: %v", err)
	}
	if _, err := e.db.ExecContext(e.t.Context(),
		`UPDATE administrators SET totp_secret=?,totp_last_step=-1 WHERE id=?`, secret, id); err != nil {
		e.t.Fatalf("store existing binding: %v", err)
	}
	return secret
}

// TestUnboundAdministratorLogsInWithPasswordAlone covers the core change of
// CW-25: an administrator without a binding needs no code at all.
func TestUnboundAdministratorLogsInWithPasswordAlone(t *testing.T) {
	e := newEnv(t)
	if state := e.security(e.adminToken); state.SecondFactorBound {
		t.Fatalf("fixture administrator should start unbound, got %+v", state)
	}
	e.loginAdmin(adminUsername, adminPassword, "")
	if !e.hasAuditAction(e.adminToken, "ADMIN_LOGIN_SUCCEEDED") {
		t.Fatal("password login was not audited")
	}
}

// TestUnboundAdministratorCannotLoginWithWrongPassword keeps the password the
// only factor an unbound account has.
func TestUnboundAdministratorCannotLoginWithWrongPassword(t *testing.T) {
	e := newEnv(t)
	e.loginAdminRaw(adminUsername, "wrong-password-entirely", "").expect(t, http.StatusUnauthorized, nil)
	e.loginAdminRaw(adminUsername, adminPassword, "").expect(t, http.StatusOK, nil)
}

// TestBoundAdministratorMustSupplyValidCode covers the other half: an already
// bound administrator still needs a working authenticator code.
func TestBoundAdministratorMustSupplyValidCode(t *testing.T) {
	e := newEnv(t)
	secret := e.setExistingBinding(adminUsername)
	if state := e.security(e.adminToken); !state.SecondFactorBound {
		t.Fatalf("existing binding was not reported: %+v", state)
	}
	e.loginAdminRaw(adminUsername, adminPassword, "").expect(t, http.StatusUnauthorized, nil)
	e.loginAdminRaw(adminUsername, adminPassword, "000000").expect(t, http.StatusUnauthorized, nil)
	e.loginAdminRaw(adminUsername, adminPassword, totpCode(secret, time.Now())).expect(t, http.StatusOK, nil)
}

// TestCLIEnrollmentBindsAnExistingAdministrator keeps the documented command a
// working way to bind an administrator that had none.
func TestCLIEnrollmentBindsAnExistingAdministrator(t *testing.T) {
	e := newEnv(t)
	secret := e.enrollAdminCLI(adminUsername)
	e.loginAdminRaw(adminUsername, adminPassword, "").expect(t, http.StatusUnauthorized, nil)
	e.loginAdminRaw(adminUsername, adminPassword, totpCode(secret, time.Now())).expect(t, http.StatusOK, nil)
}

// TestEnrollmentRequiresValidCodeBeforeActivating covers the two-step binding:
// the pending secret only becomes a login factor after a correct code arrives.
func TestEnrollmentRequiresValidCodeBeforeActivating(t *testing.T) {
	e := newEnv(t)
	enrollment := e.startEnrollment(e.adminToken)
	if state := e.security(e.adminToken); !state.EnrollmentPending || state.SecondFactorBound {
		t.Fatalf("pending enrollment should not bind yet: %+v", state)
	}
	// A wrong code neither activates the binding nor changes password login.
	e.post(e.public.URL, "/api/admin/security/enrollment/confirm", e.adminToken,
		map[string]string{"second_factor_code": "999999"}).expect(t, http.StatusUnauthorized, nil)
	e.loginAdmin(adminUsername, adminPassword, "")
	if state := e.security(e.lastAdminToken); state.SecondFactorBound {
		t.Fatalf("a failed confirmation must not bind: %+v", state)
	}
	// A correct code from the pending secret activates it.
	secret := e.pendingSecret(enrollment.Secret)
	e.post(e.public.URL, "/api/admin/security/enrollment/confirm", e.lastAdminToken,
		map[string]string{"second_factor_code": totpCode(secret, time.Now())}).expect(t, http.StatusOK, nil)
	// The completed binding revoked every session, so the account must present
	// the new factor from now on. The code just used for confirmation is spent,
	// so the next login presents the following one.
	e.loginAdminRaw(adminUsername, adminPassword, "").expect(t, http.StatusUnauthorized, nil)
	e.loginAdminRaw(adminUsername, adminPassword, totpCode(secret, time.Now().Add(30*time.Second))).
		expect(t, http.StatusOK, nil)
}

// TestAbandonedEnrollmentLeavesLoginUnchanged covers the requirement that an
// unfinished binding must not alter the current login behaviour.
func TestAbandonedEnrollmentLeavesLoginUnchanged(t *testing.T) {
	e := newEnv(t)
	e.startEnrollment(e.adminToken)
	e.loginAdmin(adminUsername, adminPassword, "")
	if state := e.security(e.lastAdminToken); state.SecondFactorBound {
		t.Fatalf("abandoned enrollment must not bind: %+v", state)
	}
}

// TestSecurityResponsesExposeNoSecretMaterial checks that the status and
// enrollment responses carry no secret and are marked uncacheable.
func TestSecurityResponsesExposeNoSecretMaterial(t *testing.T) {
	e := newEnv(t)
	status := e.get(e.public.URL, "/api/admin/security", e.adminToken).expect(t, http.StatusOK, nil)
	if cache := status.header("Cache-Control"); cache != "no-store" {
		t.Fatalf("security status must not be cached, got %q", cache)
	}
	for _, banned := range []string{"secret", "otpauth", "uri", "totp_secret"} {
		if strings.Contains(strings.ToLower(string(status.body)), banned) {
			t.Fatalf("security status leaked %q: %s", banned, status.body)
		}
	}
	enrollment := e.post(e.public.URL, "/api/admin/security/enrollment", e.adminToken, map[string]any{}).
		expect(t, http.StatusOK, nil)
	if cache := enrollment.header("Cache-Control"); cache != "no-store" {
		t.Fatalf("enrollment must not be cached, got %q", cache)
	}
	// Audit details must not carry the secret or binding URI either.
	for _, item := range e.auditEvents(e.adminToken) {
		details := strings.ToLower(item.Details)
		if strings.Contains(details, "\"secret\"") || strings.Contains(details, "otpauth") {
			t.Fatalf("audit %s leaked binding material: %s", item.Action, item.Details)
		}
	}
}

// TestUnrelatedCodeDoesNotActivateAnUnboundAccount pins down that a code is not
// a factor for an account without a binding.
func TestUnrelatedCodeDoesNotActivateAnUnboundAccount(t *testing.T) {
	e := newEnv(t)
	e.loginAdminRaw(adminUsername, adminPassword, "123456").expect(t, http.StatusOK, nil)
	if state := e.security(e.lastAdminToken); state.SecondFactorBound {
		t.Fatalf("supplying a code must not create a binding: %+v", state)
	}
}
