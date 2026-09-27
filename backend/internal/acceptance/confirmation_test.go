package acceptance

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

// SPEC-002 user stories 17, 18, 19, 20, 21, 22, 23, 24.
//
// When the same employee presents a new code for a meal that already has an
// unreversed consumption more than three seconds after the previous scan, the
// server records a pending confirmation instead of charging. The employee
// settles it from the login session that owns the flow; the terminal only ever
// observes it. The amount and deadline are the ones captured at scan time.

// pendingScan pairs a raised confirmation with the code that raised it, so a
// test can also manipulate the code while the request is outstanding.
type pendingScan struct {
	result scanResult
	token  string
	flow   string
}

// scanPending consumes the employee's first meal, then presents a code for a
// fresh flow more than three seconds later so the server records a pending
// confirmation instead of charging.
func (e *env) scanPending(t *testing.T, worker *employee, credential string) pendingScan {
	t.Helper()
	first := e.issue(worker.session, "")
	if scan := e.scan(credential, first.Token); scan.Status != "SUCCESS" {
		t.Fatalf("initial scan returned %s/%s, want SUCCESS", scan.Status, scan.Code)
	}
	e.ageFirstScanEvents(5 * time.Second)
	second := e.issue(worker.session, newPresentationID(t))
	pending := e.scan(credential, second.Token)
	if pending.Status != "PENDING" {
		t.Fatalf("repeat scan returned %s/%s, want PENDING", pending.Status, pending.Code)
	}
	if pending.PendingID == "" {
		t.Fatal("a pending confirmation has no identifier")
	}
	return pendingScan{result: pending, token: second.Token, flow: second.PresentationID}
}

// TestPendingConfirmationShowsMealAmountAndDeadline covers story 17: the request
// tells the employee which meal and amount it will charge, and by when.
func TestPendingConfirmationShowsMealAmountAndDeadline(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("D001", "13800000301", "邱枫")
	e.recharge(worker.id, 5000, "R-D001")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	pending := e.scanPending(t, worker, terminalCredential).result

	if pending.Code != "CONFIRM_REQUIRED" {
		t.Fatalf("pending scan reported code %s, want CONFIRM_REQUIRED", pending.Code)
	}
	if pending.MealCode != defaultMealCode || pending.MealName != defaultMealName {
		t.Fatalf("pending scan reported %s/%s, want %s/%s",
			pending.MealCode, pending.MealName, defaultMealCode, defaultMealName)
	}
	if pending.AmountCents != mealPrice {
		t.Fatalf("pending scan reported amount %d, want %d", pending.AmountCents, mealPrice)
	}
	if pending.ExpiresAt == nil {
		t.Fatal("pending scan reported no confirmation deadline")
	}
	if window := time.Until(*pending.ExpiresAt); window <= 0 || window > 61*time.Second {
		t.Fatalf("confirmation deadline is %s away, want about 60s", window)
	}
	if pending.EmployeeName != worker.name {
		t.Fatalf("pending scan reported employee %q, want %q", pending.EmployeeName, worker.name)
	}

	// Nothing was charged yet.
	if balance := e.employeeBalance(worker.session); balance != 5000-mealPrice {
		t.Fatalf("balance is %d before confirmation, want %d", balance, 5000-mealPrice)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 1 {
		t.Fatalf("%d consumption flows exist before confirmation, want 1", len(flows))
	}
}

// TestOnlyOwningSessionCanConfirm covers story 18: only the employee's own valid
// session may approve the charge, so bystanders cannot authorize spending.
func TestOnlyOwningSessionCanConfirm(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("D002", "13800000302", "陆遥")
	other := e.newEmployee("D003", "13800000303", "崔岳")
	e.recharge(worker.id, 5000, "R-D002")
	e.recharge(other.id, 5000, "R-D003")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	pending := e.scanPending(t, worker, terminalCredential).result

	// A different employee cannot settle it: the identifier is not even visible.
	if response, _ := e.decide(other.session, pending.PendingID, "confirm"); response.status != http.StatusNotFound {
		t.Fatalf("another employee's confirmation returned %d, want 404", response.status)
	}
	if response, _ := e.decide(other.session, pending.PendingID, "cancel"); response.status != http.StatusNotFound {
		t.Fatalf("another employee's cancellation returned %d, want 404", response.status)
	}
	// Neither can an anonymous caller.
	if response, _ := e.decide("", pending.PendingID, "confirm"); response.status != http.StatusUnauthorized {
		t.Fatalf("anonymous confirmation returned %d, want 401", response.status)
	}

	// The owning employee still can, so the pending request survived the
	// rejected attempts unchanged.
	response, result := e.decide(worker.session, pending.PendingID, "confirm")
	response.expect(t, http.StatusOK, nil)
	if result.Status != "SUCCESS" {
		t.Fatalf("the owner's confirmation returned %s/%s, want SUCCESS", result.Status, result.Code)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 2 {
		t.Fatalf("confirmed repeat produced %d consumption flows, want 2", len(flows))
	}
	if flows := e.transactions(other.id, "CONSUME"); len(flows) != 0 {
		t.Fatalf("another employee's account was charged %d times", len(flows))
	}
	if balance := e.employeeBalance(other.session); balance != 5000 {
		t.Fatalf("another employee's balance is %d, want the unchanged 5000", balance)
	}
}

// TestEmployeeCanCancelPendingConfirmation covers story 19: cancelling refuses
// the charge, leaves the account untouched, and is visible to the terminal.
func TestEmployeeCanCancelPendingConfirmation(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("D004", "13800000304", "尹娜")
	e.recharge(worker.id, 5000, "R-D004")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	pending := e.scanPending(t, worker, terminalCredential).result

	response, result := e.decide(worker.session, pending.PendingID, "cancel")
	response.expect(t, http.StatusOK, nil)
	if result.Status != "FAILED" || result.Code != "CANCELLED" {
		t.Fatalf("cancellation returned %s/%s, want FAILED/CANCELLED", result.Status, result.Code)
	}
	if result.Message == "" {
		t.Fatal("cancellation must explain itself to the employee")
	}
	if balance := e.employeeBalance(worker.session); balance != 5000-mealPrice {
		t.Fatalf("balance is %d after cancellation, want the unchanged %d", balance, 5000-mealPrice)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 1 {
		t.Fatalf("cancellation produced %d consumption flows, want the original 1", len(flows))
	}

	// The terminal observes the same refusal by identifier.
	observed, terminalView := e.terminalPending(terminalCredential, pending.PendingID)
	if observed.status == http.StatusNotFound {
		t.Fatal("the pending request disappeared instead of reporting the cancellation")
	}
	observed.expect(t, http.StatusOK, nil)
	if terminalView.Status != "FAILED" || terminalView.Code != "CANCELLED" {
		t.Fatalf("terminal observed %s/%s, want FAILED/CANCELLED", terminalView.Status, terminalView.Code)
	}

	// The employee's flow also reports the refusal, and the account keeps the
	// single original charge.
	if status := e.presentation(worker.session); status.State != "FAILED" {
		t.Fatalf("flow reports %s after cancellation, want FAILED", status.State)
	}
}

// TestPendingExpiresAtItsDeadline covers story 20: a request left unanswered
// lapses and charging it afterwards is refused, so nothing is deducted by
// accident.
func TestPendingExpiresAtItsDeadline(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("D005", "13800000305", "田甜")
	e.recharge(worker.id, 5000, "R-D005")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	pending := e.scanPending(t, worker, terminalCredential).result

	// The deadline passes without an answer.
	e.expirePending(pending.PendingID)

	response, result := e.decide(worker.session, pending.PendingID, "confirm")
	if response.status == http.StatusOK && result.Status == "SUCCESS" {
		t.Fatal("a lapsed confirmation was still approved")
	}
	response.expect(t, http.StatusOK, nil)
	if result.Status != "FAILED" {
		t.Fatalf("lapsed confirmation reports %s/%s, want FAILED", result.Status, result.Code)
	}
	if result.Code != "PENDING_EXPIRED" {
		t.Fatalf("lapsed confirmation reports code %s, want PENDING_EXPIRED", result.Code)
	}
	if balance := e.employeeBalance(worker.session); balance != 5000-mealPrice {
		t.Fatalf("balance is %d after the lapse, want the unchanged %d", balance, 5000-mealPrice)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 1 {
		t.Fatalf("%d consumption flows exist after the lapse, want 1", len(flows))
	}

	// Cancelling a lapsed request is equally harmless.
	if response, result := e.decide(worker.session, pending.PendingID, "cancel"); result.Status == "SUCCESS" {
		t.Fatalf("cancelling a lapsed request returned %s (status %d)", result.Status, response.status)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 1 {
		t.Fatalf("%d consumption flows exist, want 1", len(flows))
	}

	// Retrying needs a new code, and the meal is still chargeable afterwards.
	e.ageFirstScanEvents(5 * time.Second)
	retry := e.issue(worker.session, newPresentationID(t))
	again := e.scan(terminalCredential, retry.Token)
	if again.Status != "PENDING" {
		t.Fatalf("retry after the lapse returned %s/%s, want a fresh PENDING request", again.Status, again.Code)
	}
	if again.PendingID == pending.PendingID {
		t.Fatal("the retry reused the lapsed request instead of raising a new one")
	}
}

// TestMealEndCutsTheDeadlineShort covers story 20's second half: when the
// original meal ends before 60 seconds have passed, the request's deadline is
// that meal end, and once it passes the request lapses with the meal-end reason
// and can no longer be approved.
func TestMealEndCutsTheDeadlineShort(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("D006", "13800000306", "冯远")
	e.recharge(worker.id, 5000, "R-D006")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	pending := e.scanPending(t, worker, terminalCredential).result

	// A request raised less than 60 seconds before its meal ends carries the meal
	// end as its deadline. Once that moment has passed the request lapses, and the
	// reason reported is the meal ending rather than the 60-second timeout.
	mealEnd := time.Now().UTC().Add(-time.Second)
	e.setPendingDeadlines(pending.PendingID, mealEnd, mealEnd)

	response, result := e.decide(worker.session, pending.PendingID, "confirm")
	response.expect(t, http.StatusOK, nil)
	if result.Status == "SUCCESS" {
		t.Fatal("a confirmation was approved after the original meal had ended")
	}
	if result.Status != "FAILED" || result.Code != "MEAL_ENDED" {
		t.Fatalf("confirmation after the meal end reports %s/%s, want FAILED/MEAL_ENDED",
			result.Status, result.Code)
	}
	if balance := e.employeeBalance(worker.session); balance != 5000-mealPrice {
		t.Fatalf("balance is %d, want the unchanged %d", balance, 5000-mealPrice)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 1 {
		t.Fatalf("%d consumption flows exist, want 1", len(flows))
	}

	// The terminal observes the same lapse, and retrying needs a new code.
	_, terminalView := e.terminalPending(terminalCredential, pending.PendingID)
	if terminalView.Status != "FAILED" || terminalView.Code != "MEAL_ENDED" {
		t.Fatalf("terminal observed %s/%s, want FAILED/MEAL_ENDED", terminalView.Status, terminalView.Code)
	}

	// With the meal over, scanning again fails for lack of an active meal.
	e.disableAllMeals()
	after := e.issue(worker.session, newPresentationID(t))
	if scan := e.scan(terminalCredential, after.Token); scan.Status != "FAILED" {
		t.Fatalf("scan after the meal end returned %s/%s, want FAILED", scan.Status, scan.Code)
	}
	if balance := e.employeeBalance(worker.session); balance != 5000-mealPrice {
		t.Fatalf("balance is %d after the failed scan, want the unchanged %d", balance, 5000-mealPrice)
	}
}

// TestMealConfigChangeDoesNotInvalidatePending covers the other side of story
// 20's deadline rule: the deadline and the charge follow the snapshot taken at
// scan time, so an administrator editing the meal window while the employee is
// deciding does not cancel or extend the outstanding request.
func TestMealConfigChangeDoesNotInvalidatePending(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("D012", "13800000312", "童桦")
	e.recharge(worker.id, 5000, "R-D012")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	pending := e.scanPending(t, worker, terminalCredential).result
	deadline := *pending.ExpiresAt

	// The administrator shortens the meal window so it ends immediately.
	e.endMealNow(defaultMealCode)

	// The request keeps the deadline it was created with and is still decidable.
	if stored := e.pendingDeadline(pending.PendingID); stored != deadline.Unix() {
		t.Fatalf("stored deadline moved to %s, want the scanned %s",
			time.Unix(stored, 0).UTC(), deadline)
	}
	response, result := e.decide(worker.session, pending.PendingID, "confirm")
	response.expect(t, http.StatusOK, nil)
	if result.Status != "SUCCESS" {
		t.Fatalf("confirmation after the meal window changed returned %s/%s, want SUCCESS",
			result.Status, result.Code)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 2 {
		t.Fatalf("confirmed repeat produced %d consumption flows, want 2", len(flows))
	}
	if balance := e.employeeBalance(worker.session); balance != 5000-2*mealPrice {
		t.Fatalf("balance is %d, want %d", balance, 5000-2*mealPrice)
	}
	// The request is settled, so it can no longer be decided either way.
	if _, again := e.decide(worker.session, pending.PendingID, "cancel"); again.Code == "CANCELLED" {
		t.Fatal("a settled request was still cancellable")
	}
}

// TestDeadlineIsIndependentOfCodeRotation covers story 21: rotating or losing the
// code does not disturb a pending request, which keeps its own deadline.
func TestDeadlineIsIndependentOfCodeRotation(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("D007", "13800000307", "安然")
	e.recharge(worker.id, 5000, "R-D007")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	raised := e.scanPending(t, worker, terminalCredential)
	pending := raised.result
	originalDeadline := *pending.ExpiresAt

	// The code that raised the request expires while the request is still open.
	// The deadline recorded at scan time must not move with it.
	e.setTokenState(raised.token, "EXPIRED")

	// The employee can still confirm within the original deadline.
	response, result := e.decide(worker.session, pending.PendingID, "confirm")
	response.expect(t, http.StatusOK, nil)
	if result.Status != "SUCCESS" {
		t.Fatalf("confirmation after code rotation returned %s/%s, want SUCCESS", result.Status, result.Code)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 2 {
		t.Fatalf("confirmed repeat produced %d consumption flows, want 2", len(flows))
	}
	if balance := e.employeeBalance(worker.session); balance != 5000-2*mealPrice {
		t.Fatalf("balance is %d, want %d", balance, 5000-2*mealPrice)
	}

	// The deadline that applied was the one captured with the request.
	stored := e.pendingDeadline(pending.PendingID)
	if stored != originalDeadline.Unix() {
		t.Fatalf("stored deadline is %s, want the scanned %s",
			time.Unix(stored, 0).UTC(), originalDeadline)
	}
}

// TestConfirmationUsesScannedAmountSnapshot covers story 22: the confirmed charge
// uses the amount shown at scan time even if the administrator changes the meal
// price while the employee is deciding.
func TestConfirmationUsesScannedAmountSnapshot(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("D008", "13800000308", "韦亮")
	e.recharge(worker.id, 5000, "R-D008")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	pending := e.scanPending(t, worker, terminalCredential).result
	if pending.AmountCents != mealPrice {
		t.Fatalf("request shows amount %d, want the scanned %d", pending.AmountCents, mealPrice)
	}

	// The administrator changes the meal while the employee is deciding.
	const raised = int64(3000)
	e.reconfigureMeal(defaultMealCode, "午餐（涨价）", raised)

	response, result := e.decide(worker.session, pending.PendingID, "confirm")
	response.expect(t, http.StatusOK, nil)
	if result.Status != "SUCCESS" {
		t.Fatalf("confirmation returned %s/%s, want SUCCESS", result.Status, result.Code)
	}
	if result.AmountCents != mealPrice {
		t.Fatalf("confirmation charged %d, want the displayed %d", result.AmountCents, mealPrice)
	}
	if result.MealName != defaultMealName {
		t.Fatalf("confirmation reported meal %s, want the scanned %s", result.MealName, defaultMealName)
	}
	if balance := e.employeeBalance(worker.session); balance != 5000-2*mealPrice {
		t.Fatalf("balance is %d, want %d", balance, 5000-2*mealPrice)
	}
	flows := e.transactions(worker.id, "CONSUME")
	if len(flows) != 2 || flows[0].Amount != -mealPrice {
		t.Fatalf("consumption flows are %+v, want two charges of %d", flows, -mealPrice)
	}
}

// TestRepeatedConfirmAndCancelAreIdempotent covers story 23: repeating a
// confirmation, or retrying one whose response was lost, yields the same single
// result; a confirmation and a cancellation racing each other settle once.
func TestRepeatedConfirmAndCancelAreIdempotent(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("D009", "13800000309", "舒涵")
	e.recharge(worker.id, 5000, "R-D009")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	pending := e.scanPending(t, worker, terminalCredential).result

	// Several confirmations, one after another, all report the same charge.
	var transaction int64
	var consumptionNo string
	for attempt := 0; attempt < 3; attempt++ {
		response, result := e.decide(worker.session, pending.PendingID, "confirm")
		response.expect(t, http.StatusOK, nil)
		if result.Status != "SUCCESS" {
			t.Fatalf("confirmation %d returned %s/%s, want SUCCESS", attempt, result.Status, result.Code)
		}
		if attempt == 0 {
			transaction, consumptionNo = result.TransactionID, result.ConsumptionNo
			continue
		}
		if result.TransactionID != transaction || result.ConsumptionNo != consumptionNo {
			t.Fatalf("confirmation %d returned transaction %d/%s, want the same %d/%s",
				attempt, result.TransactionID, result.ConsumptionNo, transaction, consumptionNo)
		}
	}
	// A cancellation arriving afterwards cannot undo the settled charge.
	if response, result := e.decide(worker.session, pending.PendingID, "cancel"); result.Status == "FAILED" && result.Code == "CANCELLED" {
		t.Fatalf("cancelling a settled confirmation reported %s (status %d)", result.Code, response.status)
	}
	flows := e.transactions(worker.id, "CONSUME")
	if len(flows) != 2 {
		t.Fatalf("repeated confirmations produced %d consumption flows, want 2", len(flows))
	}
	if balance := e.employeeBalance(worker.session); balance != 5000-2*mealPrice {
		t.Fatalf("balance is %d, want %d", balance, 5000-2*mealPrice)
	}

	// A second request cancels cleanly and further cancellations stay harmless.
	e.ageFirstScanEvents(5 * time.Second)
	another := e.scan(terminalCredential, e.issue(worker.session, newPresentationID(t)).Token)
	if another.Status != "PENDING" {
		t.Fatalf("third scan returned %s/%s, want a pending request", another.Status, another.Code)
	}
	for attempt := 0; attempt < 2; attempt++ {
		response, result := e.decide(worker.session, another.PendingID, "cancel")
		response.expect(t, http.StatusOK, nil)
		if result.Status != "FAILED" || result.Code != "CANCELLED" {
			t.Fatalf("cancellation %d returned %s/%s, want FAILED/CANCELLED", attempt, result.Status, result.Code)
		}
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 2 {
		t.Fatalf("cancellations produced %d consumption flows, want 2", len(flows))
	}
}

// TestConcurrentDecisionHasOneFinalState covers story 34's confirmation half:
// confirmation and cancellation racing for the same request settle on exactly
// one outcome and one charge.
func TestConcurrentDecisionHasOneFinalState(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("D010", "13800000310", "屈扬")
	e.recharge(worker.id, 5000, "R-D010")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	pending := e.scanPending(t, worker, terminalCredential).result

	decisions := []string{"confirm", "cancel", "confirm", "cancel", "confirm", "cancel"}
	var wait sync.WaitGroup
	results := make([]scanResult, len(decisions))
	statuses := make([]apiResponse, len(decisions))
	for index, decision := range decisions {
		wait.Add(1)
		go func(slot int, choice string) {
			defer wait.Done()
			statuses[slot], results[slot] = e.decideOnce(worker.session, pending.PendingID, choice)
		}(index, decision)
	}
	wait.Wait()
	for slot, response := range statuses {
		if response.status != http.StatusOK {
			t.Fatalf("decision %d returned %d: %s", slot, response.status, response.body)
		}
	}

	// Count the settled outcomes: exactly one of the racers decides, the others
	// observe that decision.
	confirmed, cancelled := 0, 0
	var transaction int64
	for slot, result := range results {
		switch {
		case result.Status == "SUCCESS":
			confirmed++
			if transaction == 0 {
				transaction = result.TransactionID
			} else if result.TransactionID != transaction {
				t.Fatalf("decision %d reported transaction %d, want %d", slot, result.TransactionID, transaction)
			}
		case result.Code == "CANCELLED":
			cancelled++
		default:
			t.Fatalf("decision %d returned %s/%s, want a settled outcome", slot, result.Status, result.Code)
		}
	}
	if confirmed+cancelled != len(decisions) {
		t.Fatalf("%d confirmations and %d cancellations from %d decisions",
			confirmed, cancelled, len(decisions))
	}

	// The account and the flows agree with whichever outcome won.
	flows := e.transactions(worker.id, "CONSUME")
	if confirmed == 0 {
		if len(flows) != 1 {
			t.Fatalf("%d consumption flows exist after a cancellation, want the original 1", len(flows))
		}
		if balance := e.employeeBalance(worker.session); balance != 5000-mealPrice {
			t.Fatalf("balance is %d, want the unchanged %d", balance, 5000-mealPrice)
		}
	} else {
		if len(flows) != 2 || flows[0].ID != transaction {
			t.Fatalf("consumption flows are %+v, want the confirmed charge %d", flows, transaction)
		}
		if balance := e.employeeBalance(worker.session); balance != 5000-2*mealPrice {
			t.Fatalf("balance is %d, want %d", balance, 5000-2*mealPrice)
		}
	}

	// The terminal and the employee see that same single final state.
	_, terminalView := e.terminalPending(terminalCredential, pending.PendingID)
	status := e.presentation(worker.session)
	if terminalView.Status != status.Result.Status || terminalView.Code != status.Result.Code {
		t.Fatalf("terminal sees %s/%s while the employee sees %s/%s",
			terminalView.Status, terminalView.Code, status.Result.Status, status.Result.Code)
	}
}

// TestRefundedConsumptionDoesNotTriggerConfirmation covers story 24: once the
// meal's consumption has been refunded in full, presenting a new code charges it
// as a first consumption rather than asking for confirmation again.
func TestRefundedConsumptionDoesNotTriggerConfirmation(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("D011", "13800000311", "蒋川")
	e.recharge(worker.id, 5000, "R-D011")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	first := e.issue(worker.session, "")
	original := e.scan(terminalCredential, first.Token)
	if original.Status != "SUCCESS" {
		t.Fatalf("first scan returned %s/%s, want SUCCESS", original.Status, original.Code)
	}
	e.refund(original.TransactionID)

	// Well after the repeat window, a new code is charged with no confirmation.
	e.ageFirstScanEvents(5 * time.Second)
	second := e.issue(worker.session, newPresentationID(t))
	after := e.scan(terminalCredential, second.Token)
	if after.Status != "SUCCESS" {
		t.Fatalf("scan after the refund returned %s/%s, want a direct SUCCESS", after.Status, after.Code)
	}
	if after.Code == "CONFIRM_REQUIRED" {
		t.Fatal("a refunded consumption still demanded confirmation")
	}
	if after.TransactionID == original.TransactionID {
		t.Fatal("the new consumption reused the refunded transaction")
	}
	flows := e.transactions(worker.id, "CONSUME")
	if len(flows) != 2 {
		t.Fatalf("%d consumption flows exist, want 2", len(flows))
	}
	if balance := e.employeeBalance(worker.session); balance != 5000-mealPrice {
		t.Fatalf("balance is %d, want %d", balance, 5000-mealPrice)
	}

	// The refund was for the original charge only.
	refunds := e.transactions(worker.id, "REFUND")
	if len(refunds) != 1 || refunds[0].Amount != mealPrice ||
		refunds[0].RelatedTransaction != original.TransactionID {
		t.Fatalf("refunds are %+v, want one refund of the original charge %d", refunds, original.TransactionID)
	}
}

// TestConfirmationChecksFundsAtConfirmationTime covers the balance re-check: the
// pending request records an amount, but the money is only moved when the
// employee confirms, so an account drained in the meantime is refused and
// produces no consumption flow.
func TestConfirmationChecksFundsAtConfirmationTime(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("D013", "13800000313", "苗雨")
	e.recharge(worker.id, 5000, "R-D013")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	pending := e.scanPending(t, worker, terminalCredential).result
	if remaining := e.employeeBalance(worker.session); remaining != 5000-mealPrice {
		t.Fatalf("balance is %d before the withdrawal, want %d", remaining, 5000-mealPrice)
	}

	// The employee's remaining balance is removed while the request is open.
	e.adjustBalance(worker.id, -(5000 - mealPrice), "DRAIN-D013")
	if balance := e.employeeBalance(worker.session); balance != 0 {
		t.Fatalf("balance is %d after the adjustment, want 0", balance)
	}

	response, result := e.decide(worker.session, pending.PendingID, "confirm")
	response.expect(t, http.StatusOK, nil)
	if result.Status != "FAILED" || result.Code != "INSUFFICIENT_FUNDS" {
		t.Fatalf("confirmation with an empty balance returned %s/%s, want FAILED/INSUFFICIENT_FUNDS",
			result.Status, result.Code)
	}
	if result.Message == "" {
		t.Fatal("a refused confirmation must explain the reason")
	}
	if balance := e.employeeBalance(worker.session); balance != 0 {
		t.Fatalf("balance is %d after the refused confirmation, want the unchanged 0", balance)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 1 {
		t.Fatalf("%d consumption flows exist, want the original 1", len(flows))
	}

	// The terminal observes the same refusal and can act on it.
	if _, terminalView := e.terminalPending(terminalCredential, pending.PendingID); terminalView.Status != "FAILED" ||
		terminalView.Code != "INSUFFICIENT_FUNDS" {
		t.Fatalf("terminal observed %s/%s, want FAILED/INSUFFICIENT_FUNDS",
			terminalView.Status, terminalView.Code)
	}

	// A request whose funds appear before the employee decides is approved and
	// charges the amount captured at scan time.
	// The refused request is settled before a new one is raised, because at most
	// one request per employee, date and meal may be open.
	if state, _ := e.pendingState(pending.PendingID); state != "FAILED" {
		t.Fatalf("the refused request is %s, want the settled FAILED", state)
	}
	e.ageFirstScanEvents(5 * time.Second)
	next := e.issue(worker.session, newPresentationID(t))
	topped := e.scan(terminalCredential, next.Token)
	if topped.Status != "PENDING" {
		t.Fatalf("the follow-up scan returned %s/%s, want a pending request", topped.Status, topped.Code)
	}
	if topped.PendingID == pending.PendingID {
		t.Fatal("the follow-up scan reused the settled request")
	}
	e.adjustBalance(worker.id, 2*mealPrice, "TOPUP-D013")
	response, result = e.decide(worker.session, topped.PendingID, "confirm")
	response.expect(t, http.StatusOK, nil)
	if result.Status != "SUCCESS" || result.AmountCents != mealPrice {
		t.Fatalf("confirmation after funding returned %s/%s for %d, want SUCCESS for %d",
			result.Status, result.Code, result.AmountCents, mealPrice)
	}
	flows := e.transactions(worker.id, "CONSUME")
	if len(flows) != 2 || flows[0].ID != result.TransactionID {
		t.Fatalf("consumption flows are %+v, want the confirmed charge %d", flows, result.TransactionID)
	}
	if balance := e.employeeBalance(worker.session); balance != mealPrice {
		t.Fatalf("balance is %d, want %d", balance, mealPrice)
	}
}

// TestConfirmationRejectsInvalidatedSession covers the session and account checks
// made when a confirmation is submitted, so a request cannot be settled once the
// employee's authorization has gone away.
func TestConfirmationRejectsInvalidatedSession(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("D014", "13800000314", "钮兰")
	e.recharge(worker.id, 5000, "R-D014")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	// A request raised by a session that is then revoked can no longer be settled
	// through that session, and nothing is charged.
	revoked := e.scanPending(t, worker, terminalCredential).result
	e.revokeSession(worker.session)
	if response, _ := e.decide(worker.session, revoked.PendingID, "confirm"); response.status != http.StatusUnauthorized {
		t.Fatalf("confirmation from a revoked session returned %d, want 401", response.status)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 1 {
		t.Fatalf("%d consumption flows exist after the revoked confirmation, want 1", len(flows))
	}
	if balance := e.employeeBalance(e.login(worker)); balance != 5000-mealPrice {
		t.Fatalf("balance is %d after the revoked confirmation, want the unchanged %d",
			balance, 5000-mealPrice)
	}

	// A request raised while the account is still active cannot be confirmed once
	// the account stops being active. The fresh session survives the account
	// freeze, so the refusal comes from the confirmation path itself.
	//
	// The abandoned request from the revoked session is still open, and an open
	// request is unique per employee, date and meal, so it has to lapse before a
	// new one can be raised.
	e.expirePending(revoked.PendingID)
	e.ageFirstScanEvents(5 * time.Second)
	blocked := e.issue(worker.session, newPresentationID(t))
	pending := e.scan(terminalCredential, blocked.Token)
	if pending.Status != "PENDING" {
		t.Fatalf("the follow-up scan returned %s/%s, want a pending request", pending.Status, pending.Code)
	}
	e.setAccountStatus(worker.id, "FROZEN")
	response, result := e.decide(worker.session, pending.PendingID, "confirm")
	response.expect(t, http.StatusOK, nil)
	if result.Status != "FAILED" || result.Code != "ACCOUNT_UNAVAILABLE" {
		t.Fatalf("confirmation for a frozen account returned %s/%s, want FAILED/ACCOUNT_UNAVAILABLE",
			result.Status, result.Code)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 1 {
		t.Fatalf("%d consumption flows exist, want the single direct charge only", len(flows))
	}
}
