package acceptance

import (
	"net/http"
	"testing"
	"time"
)

// SPEC-002 user stories 1, 2, 3, 4, 5, 6, 7, 8.
//
// The employee presentation flow is observed through the two public endpoints
// the page uses: POST /api/me/payment-token issues a code bound to a
// presentation, and GET /api/me/payment-presentation reports the authoritative
// state of that presentation so a returning page can restore it.

// TestFirstVisitIssuesScannableCode covers story 1: the first visit to the code
// page immediately receives a usable code with server-aligned timing.
func TestFirstVisitIssuesScannableCode(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("A001", "13800000001", "张伟")
	e.recharge(worker.id, 5000, "R-A001")

	issued := e.issue(worker.session, "")

	if issued.PresentationID == "" {
		t.Fatal("first issue must bind the code to a presentation")
	}
	if len(issued.Token) != 47 || issued.Token[:4] != "pmt_" {
		t.Fatalf("issued code %q is not a server-issued payment token", issued.Token)
	}
	if !issued.ExpiresAt.After(issued.ServerTime) {
		t.Fatalf("code expiry %s must be after server time %s", issued.ExpiresAt, issued.ServerTime)
	}
	if validity := issued.ExpiresAt.Sub(issued.ServerTime); validity < 55*time.Second || validity > 65*time.Second {
		t.Fatalf("single code validity is %s, want about 60s", validity)
	}
	if !issued.RefreshAfter.After(issued.ServerTime) {
		t.Fatalf("refresh time %s must be after server time %s", issued.RefreshAfter, issued.ServerTime)
	}
	if interval := issued.RefreshAfter.Sub(issued.ServerTime); interval < 25*time.Second || interval > 35*time.Second {
		t.Fatalf("rotation interval is %s, want about 30s", interval)
	}
	if drift := time.Since(issued.ServerTime); drift < -5*time.Second || drift > 5*time.Second {
		t.Fatalf("server time %s is not aligned with the client clock", issued.ServerTime)
	}

	// The freshly issued code is immediately scannable.
	terminalCredential := e.terminal("terminal-1", "一号终端")
	result := e.scan(terminalCredential, issued.Token)
	if result.Status != "SUCCESS" {
		t.Fatalf("fresh code scan returned %s/%s, want SUCCESS", result.Status, result.Code)
	}
}

// TestReturningPageRestoresActivePresentation covers stories 2, 3 and 4: after
// leaving and returning, and after a reload, the page restores the same
// presentation instead of starting a new one, so two tabs share one flow.
func TestReturningPageRestoresActivePresentation(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("A002", "13800000002", "李娜")

	issued := e.issue(worker.session, "")

	// A returning page asks for the current presentation without a stored
	// identifier; the server must resolve the presentation already in flight.
	// A second issue inside the rotation interval must reuse the presentation
	// rather than start a new one.
	second := e.issueRaw(worker.session, "")
	if second.status != http.StatusTooManyRequests {
		t.Fatalf("refresh inside the rotation interval returned %d, want 429: %s", second.status, second.body)
	}

	restored := e.presentation(worker.session)
	if restored.PresentationID != issued.PresentationID {
		t.Fatalf("returning page restored presentation %s, want the in-flight %s",
			restored.PresentationID, issued.PresentationID)
	}
	if restored.State != "WAITING" {
		t.Fatalf("presentation state is %s, want WAITING", restored.State)
	}
	if restored.Result != (scanResult{}) {
		t.Fatalf("waiting presentation must not report a result: %+v", restored.Result)
	}

	// Two tabs issuing concurrently must resolve to the same presentation.
	for tab := 0; tab < 2; tab++ {
		var rejected struct {
			PresentationID string `json:"presentation_id"`
		}
		e.issueRaw(worker.session, "").expect(t, http.StatusTooManyRequests, &rejected)
		if rejected.PresentationID != issued.PresentationID {
			t.Fatalf("tab %d was told to use presentation %s, want %s",
				tab, rejected.PresentationID, issued.PresentationID)
		}
	}
}

// TestCodeRotationKeepsPreviousCodeUsable covers stories 5 and 11: a code is
// replaced at the server-provided refresh time, and while both the current and
// the previous code are valid, the older one still scans successfully.
func TestCodeRotationKeepsPreviousCodeUsable(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("A003", "13800000003", "王芳")
	e.recharge(worker.id, 5000, "R-A003")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	first := e.issue(worker.session, "")

	// Rotation is due 30 seconds after issue; the fixture supplies a code whose
	// issue time is already past that interval while it is still unexpired.
	rotated := e.injectToken(worker.session, first.PresentationID, time.Now().UTC().Add(-31*time.Second), 60*time.Second)
	if rotated == first.Token {
		t.Fatal("rotation fixture produced the same code value")
	}

	// The older code is still within its own validity, so it must scan.
	old := e.scan(terminalCredential, first.Token)
	if old.Status != "SUCCESS" {
		t.Fatalf("previous code scan returned %s/%s, want SUCCESS", old.Status, old.Code)
	}
	if old.ConsumptionNo == "" || old.AmountCents != mealPrice {
		t.Fatalf("previous code consumption detail is incomplete: %+v", old)
	}

	// The newer code is revoked once the meal is already consumed, so a second
	// code must not produce a second charge.
	replayed := e.scan(terminalCredential, rotated)
	if replayed.Status != "SUCCESS" || replayed.TransactionID != old.TransactionID {
		t.Fatalf("second code returned %s/%s for transaction %d, want the first transaction %d",
			replayed.Status, replayed.Code, replayed.TransactionID, old.TransactionID)
	}
	if len(e.transactions(worker.id, "CONSUME")) != 1 {
		t.Fatalf("rotating code produced %d consumption flows, want 1", len(e.transactions(worker.id, "CONSUME")))
	}
}

// TestExpiredAndMissingPresentationStates covers stories 7 and 8: an expired
// code stops being scannable, and a page whose presentation is unknown to the
// server is told to start over rather than shown a stale state.
func TestExpiredAndMissingPresentationStates(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("A004", "13800000004", "赵敏")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	issued := e.issue(worker.session, "")

	// A code past its own expiry must be refused; the page hides it instead of
	// presenting a code that would silently fail.
	expired := e.injectToken(worker.session, issued.PresentationID, time.Now().UTC().Add(-2*time.Minute), 60*time.Second)
	result := e.scan(terminalCredential, expired)
	if result.Status != "FAILED" {
		t.Fatalf("expired code scan returned %s, want FAILED", result.Status)
	}
	if len(e.transactions(worker.id, "CONSUME")) != 0 {
		t.Fatal("expired code produced a consumption flow")
	}

	// A presentation the server does not know is reported as absent so the page
	// can drop its cache and request a new flow.
	e.get(e.public.URL, "/api/me/payment-presentation?id="+newPresentationID(t), worker.session).
		expect(t, http.StatusNotFound, nil)
}

// TestPresentationStatusRequiresEmployeeOwnership covers story 18 in its
// broadest sense: presentation state is scoped to the owning employee, so
// another employee cannot observe or inherit someone else's flow.
func TestPresentationStatusRequiresEmployeeOwnership(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	owner := e.newEmployee("A005", "13800000005", "孙浩")
	other := e.newEmployee("A006", "13800000006", "周婷")

	issued := e.issue(owner.session, "")

	// The other employee sees their own (empty) presentation, never the owner's.
	e.get(e.public.URL,
		"/api/me/payment-presentation?id="+issued.PresentationID, other.session).
		expect(t, http.StatusNotFound, nil)

	otherIssued := e.issue(other.session, "")
	if otherIssued.PresentationID == issued.PresentationID {
		t.Fatal("two employees were issued the same presentation")
	}
	if otherIssued.Token == issued.Token {
		t.Fatal("two employees were issued the same code value")
	}

	// An anonymous caller cannot read presentation state at all.
	e.get(e.public.URL, "/api/me/payment-presentation", "").expect(t, http.StatusUnauthorized, nil)
	e.get(e.public.URL, "/api/me/payment-presentation", "not-a-session").expect(t, http.StatusUnauthorized, nil)
}

// TestLogoutAndRevocationClearPresentationAccess covers story 7's server side:
// once the login session is invalidated, its presentation can no longer be
// tracked and the codes it issued can no longer be scanned.
func TestLogoutAndRevocationClearPresentationAccess(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("A007", "13800000007", "吴强")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	issued := e.issue(worker.session, "")

	e.post(e.public.URL, "/api/auth/logout", worker.session, nil).expect(t, http.StatusNoContent, nil)

	e.get(e.public.URL, "/api/me/payment-presentation", worker.session).expect(t, http.StatusUnauthorized, nil)

	result := e.scan(terminalCredential, issued.Token)
	if result.Status != "FAILED" {
		t.Fatalf("code from a revoked session scanned as %s, want FAILED", result.Status)
	}
	if len(e.transactions(worker.id, "CONSUME")) != 0 {
		t.Fatal("revoked session still produced a consumption flow")
	}
}

// TestNoActiveMealFailsWithoutCharging covers the meal-boundary rule: a scan
// with no current meal period fails explicitly and moves no money.
func TestNoActiveMealFailsWithoutCharging(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("A008", "13800000008", "郑爽")
	terminalCredential := e.terminal("terminal-1", "一号终端")
	e.recharge(worker.id, 5000, "R-0001")

	issued := e.issue(worker.session, "")
	e.disableAllMeals()

	result := e.scan(terminalCredential, issued.Token)
	if result.Status != "FAILED" || result.Code != "NO_MEAL" {
		t.Fatalf("scan without an active meal returned %s/%s, want FAILED/NO_MEAL", result.Status, result.Code)
	}
	if balance := e.balance(worker.id); balance != 5000 {
		t.Fatalf("balance is %d after a rejected scan, want 5000", balance)
	}
	if len(e.transactions(worker.id, "CONSUME")) != 0 {
		t.Fatal("scan without an active meal produced a consumption flow")
	}

	// The failure is attached to that code, so rescanning reports the same
	// terminal outcome and the employee retries with a new code.
	replayed := e.scan(terminalCredential, issued.Token)
	if replayed.Status != "FAILED" || replayed.Code != "NO_MEAL" {
		t.Fatalf("rescan returned %s/%s, want the first FAILED/NO_MEAL", replayed.Status, replayed.Code)
	}
}

// TestInsufficientFundsIsFinalForTheCode covers stories 16 and 24: a code that
// fails because the balance is too low reports a definite reason, leaves no
// funds flow, and keeps reporting that outcome on rescan.
func TestInsufficientFundsIsFinalForTheCode(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("A009", "13800000009", "冯磊")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	issued := e.issue(worker.session, "")

	result := e.scan(terminalCredential, issued.Token)
	if result.Status != "FAILED" || result.Code != "INSUFFICIENT_FUNDS" {
		t.Fatalf("scan with an empty balance returned %s/%s, want FAILED/INSUFFICIENT_FUNDS",
			result.Status, result.Code)
	}
	if message := result.Message; message == "" {
		t.Fatal("failed scan must carry a reason for the employee")
	}
	if len(e.transactions(worker.id, "CONSUME")) != 0 {
		t.Fatal("failed scan produced a consumption flow")
	}

	replayed := e.scan(terminalCredential, issued.Token)
	if replayed.Status != "FAILED" || replayed.Code != "INSUFFICIENT_FUNDS" {
		t.Fatalf("rescan returned %s/%s, want the stored FAILED/INSUFFICIENT_FUNDS",
			replayed.Status, replayed.Code)
	}
	if len(e.transactions(worker.id, "CONSUME")) != 0 {
		t.Fatal("rescanning a failed code produced a consumption flow")
	}
}
