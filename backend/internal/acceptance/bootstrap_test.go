// Acceptance coverage for CW-27 (#31): the first-administrator initialization
// entry. Assertions stay at the HTTP boundary; the seeded database is the only
// way a test reaches the "no administrator yet" state.
package acceptance

import (
	"net/http"
	"strconv"
	"testing"
)

type bootstrapStatus struct {
	Required bool `json:"required"`
}

func (e *env) bootstrapStatus() bootstrapStatus {
	e.t.Helper()
	var status bootstrapStatus
	e.get(e.public.URL, "/api/bootstrap/status", "").expect(e.t, http.StatusOK, &status)
	return status
}

func (e *env) initialize(payload map[string]any) apiResponse {
	e.t.Helper()
	return e.post(e.public.URL, "/api/bootstrap", "", payload)
}

// TestInitializationOfferedOnlyWithoutAdministrators covers the visible entry
// point and its disappearance once an administrator exists. The administrator is
// created through the entry itself, so both states are observed over HTTP.
func TestInitializationOfferedOnlyWithoutAdministrators(t *testing.T) {
	e := newBareEnv(t)
	if status := e.bootstrapStatus(); !status.Required {
		t.Fatalf("empty system must offer initialization: %+v", status)
	}
	e.initialize(map[string]any{"username": "existing-admin", "password": adminPassword,
		"payment_code": true, "self_service": true}).expect(t, http.StatusCreated, nil)
	if status := e.bootstrapStatus(); status.Required {
		t.Fatalf("initialization must stop once an administrator exists: %+v", status)
	}
	e.initialize(map[string]any{"username": "sneaky", "password": adminPassword,
		"payment_code": true, "self_service": true}).expect(t, http.StatusConflict, nil)
}

// TestInitializationCreatesAdministratorModesAndSession covers the whole happy
// path: account, chosen modes, audit event and a usable session.
func TestInitializationCreatesAdministratorModesAndSession(t *testing.T) {
	e := newBareEnv(t)
	var response struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Admin       struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"administrator"`
		NextStep string `json:"next_step"`
	}
	e.initialize(map[string]any{"username": "first-admin", "password": "first-admin-password",
		"payment_code": true, "self_service": false}).expect(t, http.StatusCreated, &response)
	if response.AccessToken == "" || response.Admin.Username != "first-admin" {
		t.Fatalf("initialization did not sign the creator in: %+v", response)
	}
	if response.NextStep != "configure_meal_periods" {
		t.Fatalf("initialization should point at meal configuration, got %q", response.NextStep)
	}
	// The session works immediately.
	e.get(e.public.URL, "/api/admin/me", response.AccessToken).expect(t, http.StatusOK, nil)
	// The chosen modes are in force.
	e.expectModes(t, response.AccessToken, true, false)
	// Initialization is audited.
	if !e.hasAuditAction(response.AccessToken, "ADMIN_SYSTEM_INITIALIZED") {
		t.Fatal("initialization was not audited")
	}
	// The created account binds no second factor.
	if state := e.security(response.AccessToken); state.SecondFactorBound {
		t.Fatalf("the first administrator should start unbound: %+v", state)
	}
}

// TestInitializationRequiresAtLeastOneEntrance covers the invariant that both
// entrances cannot be off.
func TestInitializationRequiresAtLeastOneEntrance(t *testing.T) {
	e := newBareEnv(t)
	e.initialize(map[string]any{"username": "first-admin", "password": "first-admin-password",
		"payment_code": false, "self_service": false}).expect(t, http.StatusBadRequest, nil)
	if status := e.bootstrapStatus(); !status.Required {
		t.Fatal("a refused initialization must not create an administrator")
	}
}

// TestInitializationRejectsWeakInputAndCreatesNothing covers invalid input.
func TestInitializationRejectsWeakInputAndCreatesNothing(t *testing.T) {
	e := newBareEnv(t)
	for name, payload := range map[string]map[string]any{
		"short password": {"username": "first-admin", "password": "short", "payment_code": true},
		"bad username":   {"username": "x", "password": "first-admin-password", "payment_code": true},
		"empty username": {"username": "", "password": "first-admin-password", "payment_code": true},
	} {
		e.initialize(payload).expect(t, http.StatusBadRequest, nil)
		_ = name
	}
	if status := e.bootstrapStatus(); !status.Required {
		t.Fatal("refused initialization left an administrator behind")
	}
}

// TestConcurrentInitializationCreatesExactlyOneAdministrator covers the race
// requirement: only the first successful submit may create an account. Each
// racer uses a distinct username, so the winner is the one that can sign in.
func TestConcurrentInitializationCreatesExactlyOneAdministrator(t *testing.T) {
	e := newBareEnv(t)
	const attempts = 4
	const racerPassword = "racer-password-value"
	type outcome struct {
		username string
		status   int
	}
	results := make(chan outcome, attempts)
	for index := 0; index < attempts; index++ {
		go func(index int) {
			username := "racer-" + strconv.Itoa(index)
			response := e.post(e.public.URL, "/api/bootstrap", "", map[string]any{
				"username": username, "password": racerPassword,
				"payment_code": true, "self_service": true,
			})
			results <- outcome{username: username, status: response.status}
		}(index)
	}
	created, winners := 0, []string{}
	for index := 0; index < attempts; index++ {
		result := <-results
		if result.status == http.StatusCreated {
			created++
			winners = append(winners, result.username)
		}
	}
	if created != 1 {
		t.Fatalf("exactly one initialization should succeed, got %d", created)
	}
	// Read the outcome the way an operator would: the winner's credentials work,
	// and the account list it then reads over HTTP holds exactly that one account.
	session := e.loginAdmin(winners[0], racerPassword, "")
	if items := e.administrators(session); len(items) != 1 {
		t.Fatalf("concurrent initialization left %d administrators", len(items))
	}
}

// TestRepeatedSubmissionDoesNotCreateASecondAccount covers the refresh/idempotence
// requirement at the API level.
func TestRepeatedSubmissionDoesNotCreateASecondAccount(t *testing.T) {
	e := newBareEnv(t)
	payload := map[string]any{"username": "first-admin", "password": "first-admin-password",
		"payment_code": true, "self_service": true}
	e.initialize(payload).expect(t, http.StatusCreated, nil)
	e.initialize(payload).expect(t, http.StatusConflict, nil)
	session := e.loginAdmin("first-admin", "first-admin-password", "")
	if items := e.administrators(session); len(items) != 1 {
		t.Fatalf("repeated submission created %d administrators", len(items))
	}
}
