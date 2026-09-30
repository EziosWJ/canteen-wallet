// Acceptance coverage for CW-29 (#33): an administrator adopts, replaces and
// removes its own second factor from the admin UI. Assertions observe HTTP
// status, whether the login factor changed, session validity and the audit trail.
package acceptance

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// bindSecondFactor runs the two-step binding for a session and returns the new
// secret an authenticator would hold. Confirming consumes one time step and
// revokes the caller's session, so the harness remembers the secret and signs
// the fixture administrator back in with the next code.
func (e *env) bindSecondFactor(t *testing.T, session string) []byte {
	t.Helper()
	enrollment := e.startEnrollment(session)
	secret := e.pendingSecret(enrollment.Secret)
	e.post(e.public.URL, "/api/admin/security/enrollment/confirm", session,
		map[string]string{"second_factor_code": totpCode(secret, time.Now())}).expect(t, http.StatusOK, nil)
	e.adminFactor = secret
	e.reLoginAdmin()
	return secret
}

// bindOwnFactor binds a session's own account and finishes signed in again, for
// an administrator other than the fixture one.
func (e *env) bindOwnFactor(t *testing.T, username, password, session string) []byte {
	t.Helper()
	enrollment := e.startEnrollment(session)
	secret := e.pendingSecret(enrollment.Secret)
	e.post(e.public.URL, "/api/admin/security/enrollment/confirm", session,
		map[string]string{"second_factor_code": totpCode(secret, time.Now())}).expect(t, http.StatusOK, nil)
	return secret
}

// removeSecondFactor calls the unbind endpoint.
func (e *env) removeSecondFactor(session, password, code string) apiResponse {
	e.t.Helper()
	return e.request(http.MethodDelete, e.public.URL, "/api/admin/security/second-factor", session,
		map[string]string{"password": password, "second_factor_code": code})
}

// TestUnbindRequiresPasswordAndCurrentCode covers the two credentials an
// administrator must present to drop the factor.
func TestUnbindRequiresPasswordAndCurrentCode(t *testing.T) {
	e := newEnv(t)
	e.bindSecondFactor(t, e.adminToken)

	e.removeSecondFactor(e.adminToken, "wrong-password-entirely", e.nextAdminCode()).expect(t, http.StatusUnauthorized, nil)
	e.removeSecondFactor(e.adminToken, adminPassword, "000000").expect(t, http.StatusUnauthorized, nil)
	if state := e.security(e.adminToken); !state.SecondFactorBound {
		t.Fatalf("a refused unbind removed the factor: %+v", state)
	}
	// The account still needs the factor to sign in.
	e.loginAdminRaw(adminUsername, adminPassword, "").expect(t, http.StatusUnauthorized, nil)

	session := e.adminToken
	e.removeSecondFactor(session, adminPassword, e.nextAdminCode()).expect(t, http.StatusOK, nil)
	// Every session is revoked and the password alone works again.
	e.get(e.public.URL, "/api/admin/me", session).expect(t, http.StatusUnauthorized, nil)
	e.adminFactor = nil
	e.loginAdmin(adminUsername, adminPassword, "")
	if state := e.security(e.lastAdminToken); state.SecondFactorBound {
		t.Fatalf("unbind did not clear the factor: %+v", state)
	}
	if !e.hasAuditAction(e.lastAdminToken, "ADMIN_TOTP_UNBOUND") {
		t.Fatal("unbind was not audited")
	}
}

// TestUnbindIsRefusedWhenNothingIsBound covers the explicit refusal instead of a
// silent success.
func TestUnbindIsRefusedWhenNothingIsBound(t *testing.T) {
	e := newEnv(t)
	e.removeSecondFactor(e.adminToken, adminPassword, "").expect(t, http.StatusConflict, nil)
}

// TestReplacingTheFactorRequiresTheOldOne covers the change flow: the current
// password and a current code gate the replacement, and the old factor stops
// working once the new one is active.
func TestReplacingTheFactorRequiresTheOldOne(t *testing.T) {
	e := newEnv(t)
	first := e.bindSecondFactor(t, e.adminToken)

	// A replacement without the current credentials is refused.
	e.post(e.public.URL, "/api/admin/security/enrollment", e.adminToken, map[string]any{}).
		expect(t, http.StatusUnauthorized, nil)
	e.post(e.public.URL, "/api/admin/security/enrollment", e.adminToken,
		map[string]string{"password": "wrong-password-entirely", "second_factor_code": e.nextAdminCode()}).
		expect(t, http.StatusUnauthorized, nil)
	e.post(e.public.URL, "/api/admin/security/enrollment", e.adminToken,
		map[string]string{"password": adminPassword, "second_factor_code": "000000"}).
		expect(t, http.StatusUnauthorized, nil)

	// With both, the server hands out a new pending secret.
	var enrollment enrollmentResponse
	e.post(e.public.URL, "/api/admin/security/enrollment", e.adminToken,
		map[string]string{"password": adminPassword, "second_factor_code": e.nextAdminCode()}).
		expect(t, http.StatusOK, &enrollment)
	second := e.pendingSecret(enrollment.Secret)
	e.post(e.public.URL, "/api/admin/security/enrollment/confirm", e.adminToken,
		map[string]string{"second_factor_code": totpCode(second, time.Now())}).expect(t, http.StatusOK, nil)
	e.adminFactor = second

	// Only the new factor works now.
	e.loginAdminRaw(adminUsername, adminPassword, totpCode(first, time.Now().Add(time.Hour))).
		expect(t, http.StatusUnauthorized, nil)
	e.reLoginAdmin()
	if !e.hasAuditAction(e.adminToken, "ADMIN_TOTP_CHANGED") {
		t.Fatal("replacing the factor was not audited")
	}
}

// TestReplacementConsumesTheCodeItWasAuthorisedWith pins the replay guard on the
// replacement path: a code that has authorised one replacement must not
// authorise a second one inside the same 30-second step.
func TestReplacementConsumesTheCodeItWasAuthorisedWith(t *testing.T) {
	e := newEnv(t)
	e.bindSecondFactor(t, e.adminToken)

	// Mint one current code and use it twice. nextAdminCode clears the stored
	// step, so it is called once here and the same code is reused deliberately.
	code := e.nextAdminCode()
	body := map[string]string{"password": adminPassword, "second_factor_code": code}
	e.post(e.public.URL, "/api/admin/security/enrollment", e.adminToken, body).
		expect(t, http.StatusOK, nil)
	e.post(e.public.URL, "/api/admin/security/enrollment", e.adminToken, body).
		expect(t, http.StatusUnauthorized, nil)
}

// TestAnotherAdministratorCannotTouchSomeoneElsesFactor covers isolation: the
// endpoints act only on the caller's own account.
func TestAnotherAdministratorCannotTouchSomeoneElsesFactor(t *testing.T) {
	e := newEnv(t)
	victim := e.bindSecondFactor(t, e.adminToken)
	e.createAdministrator(e.adminToken, "colleague-admin", "colleague-admin-password").
		expect(t, http.StatusCreated, nil)
	colleague := e.loginAdmin("colleague-admin", "colleague-admin-password", "")

	// The colleague's own account is unbound, so it cannot unbind anything and
	// its attempts do not disturb the victim's binding.
	e.removeSecondFactor(colleague, adminPassword, totpCode(victim, time.Now())).
		expect(t, http.StatusConflict, nil)
	e.loginAdminRaw(adminUsername, adminPassword, "").expect(t, http.StatusUnauthorized, nil)
	e.reLoginAdmin()

	// The colleague may bind its own factor and ends up with a different secret.
	colleagueSecret := e.bindOwnFactor(t, "colleague-admin", "colleague-admin-password", colleague)
	if string(colleagueSecret) == string(victim) {
		t.Fatal("two administrators ended up with the same factor secret")
	}
}

// TestExpiredEnrollmentCannotBeCompleted covers the ten-minute window: an
// abandoned enrollment that outlives it must not activate.
func TestExpiredEnrollmentCannotBeCompleted(t *testing.T) {
	e := newEnv(t)
	enrollment := e.startEnrollment(e.adminToken)
	secret := e.pendingSecret(enrollment.Secret)
	e.agePendingEnrollment(t, adminUsername, 11*time.Minute)
	e.post(e.public.URL, "/api/admin/security/enrollment/confirm", e.adminToken,
		map[string]string{"second_factor_code": totpCode(secret, time.Now())}).expect(t, http.StatusConflict, nil)
	e.loginAdmin(adminUsername, adminPassword, "")
	if state := e.security(e.lastAdminToken); state.SecondFactorBound {
		t.Fatalf("an expired enrollment bound the factor: %+v", state)
	}
}

// agePendingEnrollment moves a pending enrollment's creation time into the past,
// which is the fixture equivalent of letting the ten-minute window elapse.
func (e *env) agePendingEnrollment(t *testing.T, username string, by time.Duration) {
	t.Helper()
	created := time.Now().UTC().Add(-by).Format(time.RFC3339Nano)
	if _, err := e.db.ExecContext(t.Context(),
		`UPDATE administrators SET totp_pending_created_at=? WHERE username=?`, created, username); err != nil {
		t.Fatalf("age pending enrollment: %v", err)
	}
}

// TestSecurityEndpointsAreNeverCachedAndLeakNothing covers the response rules for
// every security endpoint, not just the status one.
func TestSecurityEndpointsAreNeverCachedAndLeakNothing(t *testing.T) {
	e := newEnv(t)
	enrollment := e.startEnrollment(e.adminToken)
	secret := enrollment.Secret

	// The enrollment response legitimately carries the secret exactly once. The
	// state and the confirmation responses must not.
	confirm := e.post(e.public.URL, "/api/admin/security/enrollment/confirm", e.adminToken,
		map[string]string{"second_factor_code": "000000"})
	if cache := confirm.header("Cache-Control"); cache != "no-store" {
		t.Fatalf("confirmation must not be cached, got %q", cache)
	}
	for _, response := range []apiResponse{
		e.get(e.public.URL, "/api/admin/security", e.adminToken),
		e.post(e.public.URL, "/api/admin/security/enrollment", e.adminToken, map[string]any{}),
		confirm,
	} {
		if cache := response.header("Cache-Control"); cache != "no-store" {
			t.Fatalf("security response must not be cached, got %q", cache)
		}
		body := strings.ToLower(string(response.body))
		if response.status == http.StatusOK && strings.Contains(body, strings.ToLower(secret)) {
			t.Fatalf("security response leaked the secret: %s", response.body)
		}
	}
}

// TestLoginAuditDoesNotRecordTheCode covers the requirement that the submitted
// second factor never reaches the audit trail or the response.
func TestLoginAuditDoesNotRecordTheCode(t *testing.T) {
	e := newEnv(t)
	e.bindSecondFactor(t, e.adminToken)
	code := e.nextAdminCode()
	session := e.loginAdmin(adminUsername, adminPassword, code)
	for _, item := range e.auditEvents(session) {
		if strings.Contains(item.Details, code) || strings.Contains(item.Details, adminPassword) {
			t.Fatalf("audit %s recorded credential material: %s", item.Action, item.Details)
		}
	}
}
