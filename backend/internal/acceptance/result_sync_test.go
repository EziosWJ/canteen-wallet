package acceptance

import (
	"net/http"
	"testing"
	"time"
)

// SPEC-002 user stories 9, 10, 12, 13, 14, 15, 25, 28, 29, 30, 31.
//
// The employee page learns what happened by asking the presentation status
// endpoint the server exposes for that flow; the terminal learns it from the
// scan response and by polling the pending identifier. Both must agree.

// TestEmployeeLearnsScanResultAfterReconnect covers stories 9, 12 and 13: an
// employee who was offline while the code was scanned still obtains the result
// and the consumption detail once the page queries again.
func TestEmployeeLearnsScanResultAfterReconnect(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("B001", "13800000101", "何静")
	e.recharge(worker.id, 5000, "R-B001")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	issued := e.issue(worker.session, "")

	// The employee's phone has no connectivity at this point: the scan happens
	// without any status query from the employee.
	scan := e.scan(terminalCredential, issued.Token)
	if scan.Status != "SUCCESS" {
		t.Fatalf("scan returned %s/%s, want SUCCESS", scan.Status, scan.Code)
	}

	// Reconnecting and asking for the flow state yields the outcome and the
	// detail needed to reconcile the charge.
	status := e.presentation(worker.session)
	if status.State != "SUCCESS" {
		t.Fatalf("presentation state is %s, want SUCCESS", status.State)
	}
	if status.PresentationID != issued.PresentationID {
		t.Fatalf("status reported presentation %s, want %s", status.PresentationID, issued.PresentationID)
	}
	result := status.Result
	if result.Status != "SUCCESS" || result.Code != "CONSUMED" {
		t.Fatalf("reported result is %s/%s, want SUCCESS/CONSUMED", result.Status, result.Code)
	}
	if result.AmountCents != mealPrice {
		t.Fatalf("reported amount is %d, want %d", result.AmountCents, mealPrice)
	}
	if result.MealCode != defaultMealCode || result.MealName != defaultMealName {
		t.Fatalf("reported meal is %s/%s, want %s/%s",
			result.MealCode, result.MealName, defaultMealCode, defaultMealName)
	}
	if result.OccurredAt == "" {
		t.Fatal("reported result has no charge time")
	}
	occurred, err := time.Parse(time.RFC3339Nano, result.OccurredAt)
	if err != nil {
		t.Fatalf("charge time %q is not a timestamp: %v", result.OccurredAt, err)
	}
	if drift := time.Since(occurred); drift < -time.Minute || drift > time.Minute {
		t.Fatalf("charge time %s is not close to the scan moment", result.OccurredAt)
	}
	if result.ConsumptionNo == "" {
		t.Fatal("reported result has no consumption number")
	}
	if result.ConsumptionNo != scan.ConsumptionNo {
		t.Fatalf("employee saw consumption %s but the terminal saw %s",
			result.ConsumptionNo, scan.ConsumptionNo)
	}
	if result.TransactionID != scan.TransactionID {
		t.Fatalf("employee saw transaction %d but the terminal saw %d",
			result.TransactionID, scan.TransactionID)
	}

	// The employee's own view of the balance agrees with the charge.
	if balance := e.employeeBalance(worker.session); balance != 5000-mealPrice {
		t.Fatalf("balance is %d, want %d", balance, 5000-mealPrice)
	}
	flows := e.transactions(worker.id, "CONSUME")
	if len(flows) != 1 || flows[0].Amount != -mealPrice {
		t.Fatalf("consumption flows are %+v, want one flow of %d", flows, -mealPrice)
	}
	if flows[0].ID != scan.TransactionID {
		t.Fatalf("stored flow %d is not the reported transaction %d", flows[0].ID, scan.TransactionID)
	}
}

// TestSuccessDetailSurvivesReload covers story 14: reloading the page must show
// the same finished result rather than a fresh, empty flow.
func TestSuccessDetailSurvivesReload(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("B002", "13800000102", "罗洋")
	e.recharge(worker.id, 5000, "R-B002")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	issued := e.issue(worker.session, "")
	if scan := e.scan(terminalCredential, issued.Token); scan.Status != "SUCCESS" {
		t.Fatalf("scan returned %s/%s, want SUCCESS", scan.Status, scan.Code)
	}

	first := e.presentationByID(worker.session, issued.PresentationID)
	// Simulate several reloads, each asking for the same flow by identifier.
	for reload := 0; reload < 3; reload++ {
		again := e.presentationByID(worker.session, issued.PresentationID)
		if again.State != "SUCCESS" {
			t.Fatalf("reload %d reported state %s, want SUCCESS", reload, again.State)
		}
		if again.Result.TransactionID != first.Result.TransactionID {
			t.Fatalf("reload %d reported transaction %d, want %d",
				reload, again.Result.TransactionID, first.Result.TransactionID)
		}
		if again.Result.ConsumptionNo != first.Result.ConsumptionNo {
			t.Fatalf("reload %d reported consumption %s, want %s",
				reload, again.Result.ConsumptionNo, first.Result.ConsumptionNo)
		}
	}

	// Reloading does not charge again.
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 1 {
		t.Fatalf("reloading produced %d consumption flows, want 1", len(flows))
	}
}

// TestEmployeeStopsWaitingAndStartsNewFlow covers stories 15 and 16: once a
// flow reaches a finished state the employee ends that flow and starts a new
// one deliberately; a failure is retried with a fresh code.
func TestEmployeeStopsWaitingAndStartsNewFlow(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("B003", "13800000103", "高鹏")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	// First attempt fails for a definite business reason and the flow ends.
	first := e.issue(worker.session, "")
	failed := e.scan(terminalCredential, first.Token)
	if failed.Status != "FAILED" || failed.Code != "INSUFFICIENT_FUNDS" {
		t.Fatalf("first scan returned %s/%s, want FAILED/INSUFFICIENT_FUNDS", failed.Status, failed.Code)
	}
	ended := e.presentationByID(worker.session, first.PresentationID)
	if ended.State != "FAILED" {
		t.Fatalf("finished flow reports state %s, want FAILED", ended.State)
	}
	if ended.Result.Message == "" {
		t.Fatal("failed flow must explain the reason to the employee")
	}

	// An immediate rescan is still inside the device repeat window, so the
	// terminal is told the recorded failure rather than starting a new attempt.
	if repeated := e.scan(terminalCredential, first.Token); repeated.Code != "INSUFFICIENT_FUNDS" {
		t.Fatalf("rescan reported %s/%s, want the recorded FAILED/INSUFFICIENT_FUNDS",
			repeated.Status, repeated.Code)
	}

	// The employee resolves the cause and starts the next flow with a new code.
	e.recharge(worker.id, 5000, "R-B003")
	second := e.issue(worker.session, newPresentationID(t))
	if second.PresentationID == first.PresentationID {
		t.Fatal("a finished flow must not be reused for the next consumption")
	}
	if second.Token == first.Token {
		t.Fatal("a finished flow issued the same code value")
	}

	// The failed flow keeps its own outcome while the new flow starts waiting.
	if status := e.presentationByID(worker.session, second.PresentationID); status.State != "WAITING" {
		t.Fatalf("new flow reports state %s, want WAITING", status.State)
	}
	if still := e.presentationByID(worker.session, first.PresentationID); still.State != "FAILED" {
		t.Fatalf("old flow now reports %s, want the original FAILED", still.State)
	}

	// A new code supplied for the same meal stays inside the fixed repeat window
	// too, so the terminal receives the first result instead of a new charge.
	immediate := e.scan(terminalCredential, second.Token)
	if immediate.Code != "INSUFFICIENT_FUNDS" || !immediate.DuplicateTrigger {
		t.Fatalf("retry inside the repeat window returned %s/%s (duplicate %t), want the first result marked duplicate",
			immediate.Status, immediate.Code, immediate.DuplicateTrigger)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 0 {
		t.Fatalf("retry inside the repeat window produced %d consumption flows, want 0", len(flows))
	}

	// Because that code was merged into the failed attempt, it carries the same
	// final result, so the employee must present yet another code to be charged.
	// Observing this pins down how much of the flow a device repeat consumes.
	third := e.issue(worker.session, newPresentationID(t))
	if status := e.presentationByID(worker.session, third.PresentationID); status.State != "WAITING" {
		t.Fatalf("third flow reports state %s, want WAITING", status.State)
	}
	e.ageFirstScanEvents(5 * time.Second)
	success := e.scan(terminalCredential, third.Token)
	if success.Status != "SUCCESS" {
		t.Fatalf("retry after the repeat window returned %s/%s, want SUCCESS", success.Status, success.Code)
	}
	if status := e.presentationByID(worker.session, third.PresentationID); status.State != "SUCCESS" {
		t.Fatalf("third flow reports %s after the successful retry, want SUCCESS", status.State)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 1 || flows[0].Amount != -mealPrice {
		t.Fatalf("retry produced consumption flows %+v, want exactly one of %d", flows, -mealPrice)
	}
}

// TestPresentationPollingIsIdempotent covers story 10 at the HTTP boundary: the
// page polls repeatedly while waiting, so repeated status queries must be
// free of side effects and always reflect the latest server state immediately.
func TestPresentationPollingIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("B004", "13800000104", "唐磊")
	e.recharge(worker.id, 5000, "R-B004")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	issued := e.issue(worker.session, "")

	// Poll while still waiting: every answer says WAITING and nothing moves.
	previous := time.Time{}
	for poll := 0; poll < 5; poll++ {
		status := e.presentation(worker.session)
		if status.State != "WAITING" {
			t.Fatalf("poll %d reported state %s, want WAITING", poll, status.State)
		}
		if !status.ServerTime.After(previous) && !previous.IsZero() {
			t.Fatalf("poll %d reported server time %s, want a time after %s",
				poll, status.ServerTime, previous)
		}
		previous = status.ServerTime
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 0 {
		t.Fatalf("polling while waiting produced %d consumption flows, want 0", len(flows))
	}

	// The scan happens between polls; the very next poll already reports it.
	if scan := e.scan(terminalCredential, issued.Token); scan.Status != "SUCCESS" {
		t.Fatalf("scan returned %s/%s, want SUCCESS", scan.Status, scan.Code)
	}
	status := e.presentation(worker.session)
	if status.State != "SUCCESS" {
		t.Fatalf("first poll after the scan reported %s, want SUCCESS", status.State)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 1 {
		t.Fatalf("polling after the scan produced %d consumption flows, want 1", len(flows))
	}
}

// TestOldCodeScanStillReportsResult covers story 11 through the employee's own
// view: scanning a rotated-out code settles the flow the employee is watching.
func TestOldCodeScanStillReportsResult(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("B005", "13800000105", "贺兰")
	e.recharge(worker.id, 5000, "R-B005")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	issued := e.issue(worker.session, "")

	// A newer code exists, so the employee is showing the newer one; the older
	// code is what the terminal happens to scan.
	rotated := e.injectToken(worker.session, issued.PresentationID, time.Now().UTC().Add(-31*time.Second), 60*time.Second)
	if scan := e.scan(terminalCredential, issued.Token); scan.Status != "SUCCESS" {
		t.Fatalf("old code scan returned %s/%s, want SUCCESS", scan.Status, scan.Code)
	}

	status := e.presentation(worker.session)
	if status.State != "SUCCESS" {
		t.Fatalf("flow settled at %s after the old code was scanned, want SUCCESS", status.State)
	}
	if status.Result.AmountCents != mealPrice || status.Result.ConsumptionNo == "" {
		t.Fatalf("flow reports an incomplete detail: %+v", status.Result)
	}

	// The rotated code can no longer charge a second time.
	if replayed := e.scan(terminalCredential, rotated); replayed.TransactionID != status.Result.TransactionID {
		t.Fatalf("rotated code produced transaction %d, want the settled %d",
			replayed.TransactionID, status.Result.TransactionID)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 1 {
		t.Fatalf("flow produced %d consumption flows, want 1", len(flows))
	}
}

// TestTerminalHasNoConfirmAction covers story 29: the terminal can read a
// pending consumption but has no endpoint that approves or cancels it, so it
// cannot authorize spending on the employee's behalf.
func TestTerminalHasNoConfirmAction(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("B006", "13800000106", "秦岭")
	e.recharge(worker.id, 5000, "R-B006")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	// Consume once, then present a new code so a confirmation is required.
	first := e.issue(worker.session, "")
	if scan := e.scan(terminalCredential, first.Token); scan.Status != "SUCCESS" {
		t.Fatalf("first scan returned %s/%s, want SUCCESS", scan.Status, scan.Code)
	}
	second := e.issue(worker.session, "")
	e.ageFirstScanEvents(5 * time.Second)
	pending := e.scan(terminalCredential, second.Token)
	if pending.Status != "PENDING" || pending.PendingID == "" {
		t.Fatalf("second scan returned %s/%s, want a PENDING result", pending.Status, pending.Code)
	}

	// The terminal can observe the waiting state by identifier.
	response, observed := e.terminalPending(terminalCredential, pending.PendingID)
	response.expect(t, http.StatusOK, nil)
	if observed.Status != "PENDING" {
		t.Fatalf("terminal observed %s, want PENDING", observed.Status)
	}
	if observed.ExpiresAt == nil {
		t.Fatal("terminal observed a pending consumption without a deadline")
	}

	// No terminal route accepts a decision on the pending consumption.
	for _, action := range []string{"confirm", "cancel", "decide"} {
		response := e.post(e.internal.URL, "/api/v1/terminal/pending/"+pending.PendingID+"/"+action,
			terminalCredential, map[string]string{"decision": "confirm"})
		if response.status != http.StatusNotFound && response.status != http.StatusMethodNotAllowed {
			t.Fatalf("terminal %s returned %d: %s", action, response.status, response.body)
		}
	}
	// Retrying the scan still only observes the waiting state: the terminal
	// cannot settle it by scanning again.
	if again := e.scan(terminalCredential, second.Token); again.Status != "PENDING" {
		t.Fatalf("rescan returned %s, want the PENDING state", again.Status)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 1 {
		t.Fatalf("terminal-side actions produced %d consumption flows, want 1", len(flows))
	}
}

// TestCrossTerminalShowsSameWaitingState covers story 28: a second terminal that
// scans the same employee's new code sees the confirmation the first terminal
// raised instead of starting a parallel charge.
func TestCrossTerminalShowsSameWaitingState(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("B007", "13800000107", "姜楠")
	e.recharge(worker.id, 5000, "R-B007")
	firstTerminal := e.terminal("terminal-1", "一号终端")
	secondTerminal := e.terminal("terminal-2", "二号终端")

	first := e.issue(worker.session, "")
	if scan := e.scan(firstTerminal, first.Token); scan.Status != "SUCCESS" {
		t.Fatalf("first scan returned %s/%s, want SUCCESS", scan.Status, scan.Code)
	}
	second := e.issue(worker.session, "")
	e.ageFirstScanEvents(5 * time.Second)
	pending := e.scan(firstTerminal, second.Token)
	if pending.Status != "PENDING" {
		t.Fatalf("second scan returned %s/%s, want PENDING", pending.Status, pending.Code)
	}

	// The other terminal scans a different code for the same meal and must land
	// on the same waiting request.
	e.ageFirstScanEvents(5 * time.Second)
	third := e.injectToken(worker.session, second.PresentationID, time.Now().UTC().Add(-31*time.Second), 60*time.Second)
	observed := e.scan(secondTerminal, third)
	if observed.Status != "PENDING" {
		t.Fatalf("second terminal observed %s/%s, want the same PENDING state", observed.Status, observed.Code)
	}
	if observed.PendingID != pending.PendingID {
		t.Fatalf("second terminal saw pending %s, want %s", observed.PendingID, pending.PendingID)
	}
	if observed.AmountCents != mealPrice {
		t.Fatalf("second terminal saw amount %d, want %d", observed.AmountCents, mealPrice)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 1 {
		t.Fatalf("two terminals produced %d consumption flows, want 1", len(flows))
	}
}

// TestTerminalResumesByPendingID covers story 30: after the terminal loses its
// connection, it recovers the final outcome by the pending identifier it kept.
func TestTerminalResumesByPendingID(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("B008", "13800000108", "谢婷")
	e.recharge(worker.id, 5000, "R-B008")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	first := e.issue(worker.session, "")
	if scan := e.scan(terminalCredential, first.Token); scan.Status != "SUCCESS" {
		t.Fatalf("first scan returned %s/%s, want SUCCESS", scan.Status, scan.Code)
	}
	second := e.issue(worker.session, "")
	e.ageFirstScanEvents(5 * time.Second)
	pending := e.scan(terminalCredential, second.Token)
	if pending.Status != "PENDING" {
		t.Fatalf("second scan returned %s/%s, want PENDING", pending.Status, pending.Code)
	}

	// The employee confirms; the terminal does not observe that response because
	// it dropped its connection at this point.
	response, decided := e.decide(worker.session, pending.PendingID, "confirm")
	response.expect(t, http.StatusOK, nil)
	if decided.Status != "SUCCESS" {
		t.Fatalf("confirmation returned %s/%s, want SUCCESS", decided.Status, decided.Code)
	}

	// Recovering by the stored identifier gives the same final outcome.
	recovered, observed := e.terminalPending(terminalCredential, pending.PendingID)
	recovered.expect(t, http.StatusOK, nil)
	if observed.Status != "SUCCESS" {
		t.Fatalf("terminal recovered %s, want SUCCESS", observed.Status)
	}
	if observed.TransactionID != decided.TransactionID {
		t.Fatalf("terminal recovered transaction %d, want %d", observed.TransactionID, decided.TransactionID)
	}
	if observed.ConsumptionNo != decided.ConsumptionNo {
		t.Fatalf("terminal recovered consumption %s, want %s", observed.ConsumptionNo, decided.ConsumptionNo)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 2 {
		t.Fatalf("confirmed repeat produced %d consumption flows, want 2", len(flows))
	}
}

// TestUnreachableServerRejectsWithoutOfflineDeduction covers story 31: with the
// server down the terminal cannot obtain any result and must not settle the
// charge locally.
func TestUnreachableServerRejectsWithoutOfflineDeduction(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("B009", "13800000109", "尹超")
	e.recharge(worker.id, 5000, "R-B009")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	issued := e.issue(worker.session, "")
	before := e.employeeBalance(worker.session)

	e.stopInternal()
	if err := e.scanWithoutServer(terminalCredential, issued.Token); err == nil {
		t.Fatal("scan against an unreachable server unexpectedly reported a result")
	}

	// The server is back. Nothing was charged while it was away, and the code is
	// still usable, so the terminal can simply retry.
	if balance := e.employeeBalance(worker.session); balance != before {
		t.Fatalf("balance is %d after an offline scan, want the unchanged %d", balance, before)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 0 {
		t.Fatalf("an offline scan produced %d consumption flows, want 0", len(flows))
	}
	if events := e.scanEvents(); len(events) != 0 {
		t.Fatalf("an offline scan produced %d server-side scan events, want 0", len(events))
	}

	// A reachable retry of the same code settles normally.
	e.startInternal()
	if retry := e.scan(terminalCredential, issued.Token); retry.Status != "SUCCESS" {
		t.Fatalf("retry after the outage returned %s/%s, want SUCCESS", retry.Status, retry.Code)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 1 {
		t.Fatalf("retry after the outage produced %d consumption flows, want 1", len(flows))
	}
}

// TestTerminalSeesEveryScanOutcome covers story 25 from the terminal's side: the
// device is given the outcome of its own scan and can read back the trace of the
// scans it performed, including the consumption it recorded.
func TestTerminalSeesEveryScanOutcome(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("B010", "13800000110", "康宁")
	terminalCredential := e.terminal("terminal-1", "一号终端")
	otherCredential := e.terminal("terminal-2", "二号终端")

	// A scan with no funds reports a definite failure and no consumption.
	first := e.issue(worker.session, "")
	failed := e.scan(terminalCredential, first.Token)
	if failed.Status != "FAILED" || failed.Code != "INSUFFICIENT_FUNDS" || failed.TransactionID != 0 {
		t.Fatalf("scan without funds returned %+v, want a FAILED result without a transaction", failed)
	}
	if failed.ConsumptionNo != "" || failed.OccurredAt != "" {
		t.Fatalf("failed scan reported consumption details: %+v", failed)
	}

	// A scan with funds reports the consumption it created.
	e.recharge(worker.id, 5000, "R-B010")
	e.ageFirstScanEvents(5 * time.Second)
	second := e.issue(worker.session, newPresentationID(t))
	succeeded := e.scan(terminalCredential, second.Token)
	if succeeded.Status != "SUCCESS" || succeeded.TransactionID == 0 {
		t.Fatalf("funded scan returned %+v, want a SUCCESS with a transaction", succeeded)
	}
	if succeeded.AmountCents != mealPrice || succeeded.MealName != defaultMealName {
		t.Fatalf("funded scan reported %s/%d, want %s/%d",
			succeeded.MealName, succeeded.AmountCents, defaultMealName, mealPrice)
	}

	// A scan needing confirmation reports the waiting state with its deadline.
	e.ageFirstScanEvents(5 * time.Second)
	third := e.issue(worker.session, newPresentationID(t))
	pending := e.scan(terminalCredential, third.Token)
	if pending.Status != "PENDING" || pending.PendingID == "" || pending.ExpiresAt == nil {
		t.Fatalf("repeat scan returned %+v, want a PENDING result with a deadline", pending)
	}
	if pending.AmountCents != mealPrice {
		t.Fatalf("pending scan reported amount %d, want %d", pending.AmountCents, mealPrice)
	}

	// The terminal's own event feed carries the outcomes it observed, while the
	// other terminal only sees what it did itself.
	var feed struct {
		Items []scanEvent `json:"items"`
	}
	e.terminalEvents(terminalCredential, 0, &feed)
	if len(feed.Items) != 3 {
		t.Fatalf("the terminal was told about %d of its scans, want 3: %+v", len(feed.Items), feed.Items)
	}
	var codes []string
	for _, event := range feed.Items {
		if event.TerminalID != "terminal-1" {
			t.Fatalf("the terminal was told about an event from %s", event.TerminalID)
		}
		codes = append(codes, event.ResultCode)
	}
	for _, want := range []string{"INSUFFICIENT_FUNDS", "CONSUMED", "CONFIRM_REQUIRED"} {
		found := false
		for _, code := range codes {
			if code == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("the terminal's event feed %v does not report %s", codes, want)
		}
	}

	var otherFeed struct {
		Items []scanEvent `json:"items"`
	}
	e.terminalEvents(otherCredential, 0, &otherFeed)
	if len(otherFeed.Items) != 0 {
		t.Fatalf("the other terminal was told about %d scans it did not perform", len(otherFeed.Items))
	}

	// A terminal without credentials cannot read any of it.
	e.get(e.internal.URL, "/api/v1/terminal/events", "").expect(t, http.StatusUnauthorized, nil)
	e.get(e.internal.URL, "/api/v1/terminal/events", "not-a-terminal").expect(t, http.StatusUnauthorized, nil)
}

// TestTerminalResumesAllOutstandingConfirmations covers story 30's other half: a
// terminal that restarts can recover every request it still has to resolve, not
// just the most recent one.
func TestTerminalResumesAllOutstandingConfirmations(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	terminalCredential := e.terminal("terminal-1", "一号终端")

	// One terminal serves two employees, each of whom ends up with an outstanding
	// confirmation. A single employee cannot hold two at once: while a request is
	// open the service refuses to start another presentation for that employee.
	workers := []*employee{
		e.newEmployee("B011", "13800000111", "宋雨"),
		e.newEmployee("B012", "13800000112", "毕凡"),
	}
	requests := make([]string, 0, len(workers))
	for index, worker := range workers {
		e.recharge(worker.id, 5000, "R-B01"+string(rune('1'+index)))
		first := e.issue(worker.session, "")
		if scan := e.scan(terminalCredential, first.Token); scan.Status != "SUCCESS" {
			t.Fatalf("employee %d first scan returned %s/%s, want SUCCESS", index, scan.Status, scan.Code)
		}
		e.ageFirstScanEvents(5 * time.Second)
		next := e.issue(worker.session, newPresentationID(t))
		pending := e.scan(terminalCredential, next.Token)
		if pending.Status != "PENDING" {
			t.Fatalf("employee %d repeat scan returned %s/%s, want PENDING", index, pending.Status, pending.Code)
		}
		requests = append(requests, pending.PendingID)
	}

	// While a request is open, the employee cannot start another presentation.
	if response := e.issueRaw(workers[0].session, newPresentationID(t)); response.status != http.StatusConflict {
		t.Fatalf("starting a presentation while a request is open returned %d, want 409: %s",
			response.status, response.body)
	}

	// The restarted terminal recovers both outstanding requests.
	var recovered struct {
		Items []scanResult `json:"items"`
	}
	e.get(e.internal.URL, "/api/v1/terminal/pending/recent", terminalCredential).
		expect(t, http.StatusOK, &recovered)
	if len(recovered.Items) != len(requests) {
		t.Fatalf("the terminal recovered %d outstanding requests, want %d: %+v",
			len(recovered.Items), len(requests), recovered.Items)
	}
	seen := map[string]bool{}
	for _, item := range recovered.Items {
		if item.Status != "PENDING" {
			t.Fatalf("recovered request %s reports %s, want PENDING", item.PendingID, item.Status)
		}
		seen[item.PendingID] = true
	}
	for _, id := range requests {
		if !seen[id] {
			t.Fatalf("the terminal did not recover outstanding request %s", id)
		}
	}

	// One employee confirms, the other cancels: the terminal can recover both
	// settled outcomes after another restart.
	if response, result := e.decide(workers[0].session, requests[0], "confirm"); result.Status != "SUCCESS" {
		t.Fatalf("confirmation returned %s/%s (status %d)", result.Status, result.Code, response.status)
	}
	if response, result := e.decide(workers[1].session, requests[1], "cancel"); result.Code != "CANCELLED" {
		t.Fatalf("cancellation returned %s/%s (status %d)", result.Status, result.Code, response.status)
	}

	e.get(e.internal.URL, "/api/v1/terminal/pending/recent", terminalCredential).
		expect(t, http.StatusOK, &recovered)
	outcomes := map[string]string{}
	for _, item := range recovered.Items {
		outcomes[item.PendingID] = item.Status + "/" + item.Code
	}
	if outcomes[requests[0]] != "SUCCESS/CONSUMED" {
		t.Fatalf("the confirmed request reports %s, want SUCCESS/CONSUMED", outcomes[requests[0]])
	}
	if outcomes[requests[1]] != "FAILED/CANCELLED" {
		t.Fatalf("the cancelled request reports %s, want FAILED/CANCELLED", outcomes[requests[1]])
	}

	// The balances and the flows reflect exactly the confirmed charges.
	for index, worker := range workers {
		want := int64(5000 - mealPrice)
		if index == 0 {
			want = 5000 - 2*mealPrice
		}
		if balance := e.employeeBalance(worker.session); balance != want {
			t.Fatalf("employee %d balance is %d, want %d", index, balance, want)
		}
		wantFlows := 1
		if index == 0 {
			wantFlows = 2
		}
		if flows := e.transactions(worker.id, "CONSUME"); len(flows) != wantFlows {
			t.Fatalf("employee %d has %d consumption flows, want %d", index, len(flows), wantFlows)
		}
	}

	// A terminal without credentials cannot read any of it.
	e.get(e.internal.URL, "/api/v1/terminal/pending/recent", "").expect(t, http.StatusUnauthorized, nil)
	e.get(e.internal.URL, "/api/v1/terminal/pending/recent", "not-a-terminal").expect(t, http.StatusUnauthorized, nil)
}
