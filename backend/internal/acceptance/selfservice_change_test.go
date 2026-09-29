package acceptance

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

// SPEC-003 stories 7, 8, 9, 10, 11, 15, 17, 18, 19, 20, 21, 25, 26, 29, 34.
//
// Confirming is only allowed when the preview the employee saw is still true.
// These tests drive the refusal and change paths, the shared counting rule, and
// the race between a scan confirmation and opening the self-service page.

// --- stories 7, 8, 9, 10, 11: the existing-consumption count -----------------

// TestSelfServiceCountsScanSelfServiceAndManualSupply covers stories 7, 10 and
// 15: the count includes every entrance that produced an unreversed
// consumption, and each self-service confirmation needs its own explicit tap.
func TestSelfServiceCountsScanSelfServiceAndManualSupply(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("C001", "13800000501", "许一")
	e.recharge(worker.id, 20000, "R-C001")
	terminalCredential := e.terminal("terminal-count-1", "计数终端")

	// One terminal scan.
	token := e.issue(worker.session, "")
	if scan := e.scan(terminalCredential, token.Token); scan.Status != "SUCCESS" {
		t.Fatalf("scan returned %s/%s, want SUCCESS", scan.Status, scan.Code)
	}

	// Story 8: after a single consumption the preview says so and still allows
	// another one.
	first := e.openSelfService(worker.session)
	if first.ExistingCount != 1 {
		t.Fatalf("preview reports %d consumptions after a scan, want 1", first.ExistingCount)
	}
	_, confirmed := e.confirmSelfService(worker.session, first.IntentID)
	if confirmed.Status != "SUCCESS" {
		t.Fatalf("confirming after a scan returned %s/%s, want SUCCESS", confirmed.Status, confirmed.Code)
	}
	if confirmed.Consumption == nil || confirmed.Consumption.ConsumptionNo == "" {
		t.Fatal("second consumption reported no consumption number")
	}

	// Story 15 and 26: a further self-service consumption requires a fresh intent
	// and another explicit confirmation.
	second := e.openSelfService(worker.session)
	if second.ExistingCount != 2 {
		t.Fatalf("preview reports %d consumptions after self-service, want 2", second.ExistingCount)
	}
	if second.IntentID == first.IntentID {
		t.Fatalf("preview reused intent %q for a new consumption, want a new intent", second.IntentID)
	}
	_, confirmedAgain := e.confirmSelfService(worker.session, second.IntentID)
	if confirmedAgain.Status != "SUCCESS" {
		t.Fatalf("second self-service confirmation returned %s/%s, want SUCCESS", confirmedAgain.Status, confirmedAgain.Code)
	}

	// Story 9: a manual-supply posting is a consumption too, so the running
	// total must be reported exactly, not off by one.
	e.postManualSupply(worker.id, "MS-C001", time.Now().In(e.location).Format("2006-01-02"), mealPrice)
	third := e.openSelfService(worker.session)
	if third.ExistingCount != 4 {
		t.Fatalf("preview reports %d consumptions after manual supply, want 4", third.ExistingCount)
	}

	// Every entrance is distinguishable in the ledger (story 31 context).
	entries := e.transactions(worker.id, "CONSUME")
	sources := map[string]int{}
	for _, entry := range entries {
		sources[entry.BusinessType]++
	}
	if sources["SELF_SERVICE"] != 2 || sources["MEAL_PERIOD"] != 1 || sources["MANUAL_SUPPLY"] != 1 {
		t.Fatalf("ledger sources are %v, want 2 self-service, 1 scan and 1 manual supply", sources)
	}
}

// TestSelfServiceCountExcludesRefundedConsumption covers story 11: a fully
// refunded consumption no longer counts as an existing consumption.
func TestSelfServiceCountExcludesRefundedConsumption(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("C002", "13800000502", "何二")
	e.recharge(worker.id, 20000, "R-C002")
	terminalCredential := e.terminal("terminal-count-2", "退款计数终端")

	token := e.issue(worker.session, "")
	if scan := e.scan(terminalCredential, token.Token); scan.Status != "SUCCESS" {
		t.Fatalf("scan returned %s/%s, want SUCCESS", scan.Status, scan.Code)
	}
	entries := e.transactions(worker.id, "CONSUME")
	if len(entries) != 1 {
		t.Fatalf("employee has %d consumptions, want 1", len(entries))
	}
	e.refund(entries[0].ID)

	preview := e.openSelfService(worker.session)
	if preview.ExistingCount != 0 {
		t.Fatalf("preview reports %d consumptions after a full refund, want 0", preview.ExistingCount)
	}
}

// TestSelfServiceCountIgnoresWaitingScanRequest covers story 7's other half: a
// request still waiting for confirmation is not a consumption.
func TestSelfServiceCountIgnoresWaitingScanRequest(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("C003", "13800000503", "吕三")
	e.recharge(worker.id, 20000, "R-C003")
	terminalCredential := e.terminal("terminal-count-3", "待确认计数终端")

	// A first scan charges; a second one beyond the dedupe window waits.
	pending := e.scanPending(t, worker, terminalCredential)

	// Opening the page cancels the waiting request, and the count reflects only
	// the consumption that actually happened.
	preview := e.openSelfService(worker.session)
	if preview.ExistingCount != 1 {
		t.Fatalf("preview reports %d consumptions with a waiting request, want 1", preview.ExistingCount)
	}
	_, status := e.terminalPending(terminalCredential, pending.result.PendingID)
	if status.Code != "CANCELLED" {
		t.Fatalf("waiting request ended as %s/%s, want CANCELLED", status.Status, status.Code)
	}
}

// --- stories 17, 18, 19: refusing and re-confirming -------------------------

// TestSelfServiceRefusesWhenMealEndedAfterPreview covers story 17: if the meal
// ends while the employee is looking at the preview, the charge is refused and
// nothing moves.
func TestSelfServiceRefusesWhenMealEndedAfterPreview(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("C004", "13800000504", "施四")
	e.recharge(worker.id, 20000, "R-C004")

	preview := e.openSelfService(worker.session)
	if preview.Status != "READY" {
		t.Fatalf("preview status is %s, want READY", preview.Status)
	}
	e.endMealNow(defaultMealCode)

	_, outcome := e.confirmSelfService(worker.session, preview.IntentID)
	if outcome.Status != "REJECTED" {
		t.Fatalf("confirmation after the meal ended returned %s/%s, want REJECTED", outcome.Status, outcome.Code)
	}
	if outcome.Consumption != nil {
		t.Fatal("refused confirmation still reported a consumption")
	}
	if balance := e.employeeBalance(worker.session); balance != 20000 {
		t.Fatalf("balance is %d after a refused confirmation, want 20000", balance)
	}
	if consumers := len(e.transactions(worker.id, "CONSUME")); consumers != 0 {
		t.Fatalf("refused confirmation wrote %d consumptions, want 0", consumers)
	}
	// A retry must not charge either: the intent is finished.
	_, retried := e.confirmSelfService(worker.session, preview.IntentID)
	if retried.Status == "SUCCESS" {
		t.Fatal("retrying a refused confirmation charged the employee")
	}
	if balance := e.employeeBalance(worker.session); balance != 20000 {
		t.Fatalf("balance is %d after retrying a refusal, want 20000", balance)
	}
}

// TestSelfServiceRepreviewsWhenPriceChanged covers story 18: when the
// administrator changes the price after the preview, the confirmation does not
// charge the stale amount; it returns the new preview and only a second
// confirmation charges.
func TestSelfServiceRepreviewsWhenPriceChanged(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("C005", "13800000505", "孔五")
	e.recharge(worker.id, 20000, "R-C005")

	preview := e.openSelfService(worker.session)
	e.reconfigureMeal(defaultMealCode, "午餐改价", 2000)

	_, changed := e.confirmSelfService(worker.session, preview.IntentID)
	if changed.Status != "CONFIRM_REQUIRED" {
		t.Fatalf("confirmation after a price change returned %s/%s, want CONFIRM_REQUIRED", changed.Status, changed.Code)
	}
	if changed.Preview == nil {
		t.Fatal("the changed confirmation returned no updated preview")
	}
	if changed.Preview.AmountCents != 2000 || changed.Preview.MealName != "午餐改价" {
		t.Fatalf("updated preview is %s/%d, want 午餐改价/2000",
			changed.Preview.MealName, changed.Preview.AmountCents)
	}
	if balance := e.employeeBalance(worker.session); balance != 20000 {
		t.Fatalf("balance is %d after the stale confirmation, want 20000 (no charge)", balance)
	}
	if consumers := len(e.transactions(worker.id, "CONSUME")); consumers != 0 {
		t.Fatalf("the stale confirmation wrote %d consumptions, want 0", consumers)
	}

	// The refreshed preview is what the employee confirms, at the new price.
	_, confirmed := e.confirmSelfService(worker.session, changed.Preview.IntentID)
	if confirmed.Status != "SUCCESS" {
		t.Fatalf("confirming the updated preview returned %s/%s, want SUCCESS", confirmed.Status, confirmed.Code)
	}
	if confirmed.Consumption == nil || confirmed.Consumption.AmountCents != 2000 {
		t.Fatalf("consumption charged %v, want 2000", confirmed.Consumption)
	}
	if balance := e.employeeBalance(worker.session); balance != 20000-2000 {
		t.Fatalf("balance is %d, want %d", balance, 20000-2000)
	}
}

// TestSelfServiceRepreviewsWhenCountChanged covers story 19: when another
// entrance consumes the same meal between preview and confirmation, the employee
// is shown the new count instead of being charged unknowingly.
func TestSelfServiceRepreviewsWhenCountChanged(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("C006", "13800000506", "曹六")
	e.recharge(worker.id, 20000, "R-C006")
	terminalCredential := e.terminal("terminal-count-4", "次数变化终端")

	preview := e.openSelfService(worker.session)
	if preview.ExistingCount != 0 {
		t.Fatalf("preview reports %d consumptions, want 0", preview.ExistingCount)
	}

	// The employee scans at the terminal instead, so the count moves on.
	token := e.issue(worker.session, "")
	if scan := e.scan(terminalCredential, token.Token); scan.Status != "SUCCESS" {
		t.Fatalf("scan returned %s/%s, want SUCCESS", scan.Status, scan.Code)
	}

	_, changed := e.confirmSelfService(worker.session, preview.IntentID)
	if changed.Status != "CONFIRM_REQUIRED" {
		t.Fatalf("confirmation after the count changed returned %s/%s, want CONFIRM_REQUIRED", changed.Status, changed.Code)
	}
	if changed.Preview == nil || changed.Preview.ExistingCount != 1 {
		t.Fatalf("updated preview is %v, want existing_count 1", changed.Preview)
	}
	if consumers := len(e.transactions(worker.id, "CONSUME")); consumers != 1 {
		t.Fatalf("employee has %d consumptions, want the scan only", consumers)
	}
}

// --- stories 20, 21: funds and account refusals -----------------------------

// TestSelfServiceRefusesWithInsufficientFunds covers story 20: an exhausted
// account is refused cleanly and can be topped up and retried.
func TestSelfServiceRefusesWithInsufficientFunds(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("C007", "13800000507", "严七")
	e.recharge(worker.id, 1000, "R-C007")

	preview := e.openSelfService(worker.session)
	_, refused := e.confirmSelfService(worker.session, preview.IntentID)
	if refused.Status != "REJECTED" || refused.Code != "INSUFFICIENT_FUNDS" {
		t.Fatalf("confirmation returned %s/%s, want REJECTED/INSUFFICIENT_FUNDS", refused.Status, refused.Code)
	}
	if balance := e.employeeBalance(worker.session); balance != 1000 {
		t.Fatalf("balance is %d after a refused charge, want 1000", balance)
	}
	if consumers := len(e.transactions(worker.id, "CONSUME")); consumers != 0 {
		t.Fatalf("refused charge wrote %d consumptions, want 0", consumers)
	}

	// After topping up, a fresh preview and confirmation succeed.
	e.recharge(worker.id, 5000, "R-C007-B")
	fresh := e.openSelfService(worker.session)
	_, confirmed := e.confirmSelfService(worker.session, fresh.IntentID)
	if confirmed.Status != "SUCCESS" {
		t.Fatalf("confirmation after topping up returned %s/%s, want SUCCESS", confirmed.Status, confirmed.Code)
	}
	if balance := e.employeeBalance(worker.session); balance != 6000-mealPrice {
		t.Fatalf("balance is %d, want %d", balance, 6000-mealPrice)
	}
}

// TestSelfServiceRefusesWhenAccountUnavailable covers story 21: a frozen account
// cannot be charged, and the page says so instead of failing obscurely.
func TestSelfServiceRefusesWhenAccountUnavailable(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("C008", "13800000508", "华八")
	e.recharge(worker.id, 20000, "R-C008")

	preview := e.openSelfService(worker.session)
	e.setAccountStatus(worker.id, "FROZEN")

	_, refused := e.confirmSelfService(worker.session, preview.IntentID)
	if refused.Status != "REJECTED" || refused.Code != "ACCOUNT_UNAVAILABLE" {
		t.Fatalf("confirmation returned %s/%s, want REJECTED/ACCOUNT_UNAVAILABLE", refused.Status, refused.Code)
	}
	if balance := e.employeeBalance(worker.session); balance != 20000 {
		t.Fatalf("balance is %d after a refused charge, want 20000", balance)
	}
	// The preview must also stop offering a confirmation.
	frozen := e.openSelfService(worker.session)
	if frozen.Status != "UNAVAILABLE" {
		t.Fatalf("preview status is %s for a frozen account, want UNAVAILABLE", frozen.Status)
	}
}

// TestSelfServiceRefusesWhenSessionRevoked covers story 21's login half: a
// revoked session cannot confirm, and nothing is charged.
func TestSelfServiceRefusesWhenSessionRevoked(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("C009", "13800000509", "金九")
	e.recharge(worker.id, 20000, "R-C009")

	preview := e.openSelfService(worker.session)
	e.revokeSession(worker.session)

	response, _ := e.confirmSelfService(worker.session, preview.IntentID)
	if response.status != http.StatusUnauthorized {
		t.Fatalf("confirmation with a revoked session returned %d, want 401", response.status)
	}
	// A new login is required; the balance is untouched.
	restored := e.login(worker)
	if balance := e.employeeBalance(restored); balance != 20000 {
		t.Fatalf("balance is %d after an unauthorized confirmation, want 20000", balance)
	}
}

// --- story 34: cross-employee isolation -------------------------------------

// TestSelfServiceCannotReadOrConfirmAnotherEmployee covers story 34: another
// employee's preview, intent and consumption result are all invisible, and an
// unknown intent is indistinguishable from a missing one.
func TestSelfServiceCannotReadOrConfirmAnotherEmployee(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	victim := e.newEmployee("C010", "13800000510", "魏十")
	attacker := e.newEmployee("C011", "13800000511", "陶十一")
	e.recharge(victim.id, 20000, "R-C010")
	e.recharge(attacker.id, 20000, "R-C011")

	victimPreview := e.openSelfService(victim.session)
	if victimPreview.IntentID == "" {
		t.Fatal("victim preview returned no intent")
	}

	// The attacker cannot confirm the victim's intent.
	response, _ := e.confirmSelfService(attacker.session, victimPreview.IntentID)
	if response.status != http.StatusNotFound {
		t.Fatalf("cross-employee confirmation returned %d, want 404", response.status)
	}
	resultResponse, _ := e.selfServiceResult(attacker.session, victimPreview.IntentID)
	if resultResponse.status != http.StatusNotFound {
		t.Fatalf("cross-employee result read returned %d, want 404", resultResponse.status)
	}
	// An obviously unknown intent looks the same.
	unknownResponse, _ := e.confirmSelfService(attacker.session, "ssi_00000000000000000000000000000000")
	if unknownResponse.status != response.status {
		t.Fatalf("unknown intent returned %d but another employee's returned %d; they must be indistinguishable",
			unknownResponse.status, response.status)
	}
	// The attacker's own preview is unaffected and the victim was not charged.
	own := e.openSelfService(attacker.session)
	if own.ExistingCount != 0 {
		t.Fatalf("attacker preview reports %d consumptions, want 0", own.ExistingCount)
	}
	if balance := e.employeeBalance(victim.session); balance != 20000 {
		t.Fatalf("victim balance is %d after a cross-employee attempt, want 20000", balance)
	}
}

// --- story 29: cancel racing a scan confirmation ----------------------------

// TestSelfServiceCancelledRequestCannotBeConfirmed covers the other side of
// story 29 deterministically: once the page entry has cancelled the waiting scan
// request, confirming that request cannot charge the account.
func TestSelfServiceCancelledRequestCannotBeConfirmed(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("C014", "13800000514", "谢十四")
	e.recharge(worker.id, 20000, "R-C014")
	terminalCredential := e.terminal("terminal-race-2", "竞态终端二")

	pending := e.scanPending(t, worker, terminalCredential)
	afterFirstScan := 20000 - mealPrice

	// The cancellation wins: the page entry runs first.
	preview := e.openSelfService(worker.session)
	if preview.Status != "READY" || preview.ExistingCount != 1 {
		t.Fatalf("preview after cancelling is %v, want READY with 1 prior consumption", preview)
	}

	// The late scan confirmation must not charge.
	_, confirmed := e.decide(worker.session, pending.result.PendingID, "confirm")
	if confirmed.Status == "SUCCESS" {
		t.Fatal("the scan confirmation charged after the page entry cancelled it")
	}
	if balance := e.employeeBalance(worker.session); balance != afterFirstScan {
		t.Fatalf("balance is %d after the late confirmation, want %d", balance, afterFirstScan)
	}
	if consumers := len(e.transactions(worker.id, "CONSUME")); consumers != 1 {
		t.Fatalf("employee has %d consumptions, want the first scan only", consumers)
	}
}

// TestSelfServiceEntryRacingScanConfirmHasOneOutcome covers story 29: when
// opening the self-service page races the employee confirming the scan request
// in the terminal flow, the server decides one final outcome and at most one
// consumption exists.
func TestSelfServiceEntryRacingScanConfirmHasOneOutcome(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("C012", "13800000512", "姜十二")
	e.recharge(worker.id, 20000, "R-C012")
	terminalCredential := e.terminal("terminal-race-1", "竞态终端")

	pending := e.scanPending(t, worker, terminalCredential)

	var wg sync.WaitGroup
	var cancelPreview selfServicePreview
	var entryResponse apiResponse
	var confirmed scanResult
	wg.Add(2)
	go func() {
		defer wg.Done()
		entryResponse, cancelPreview = e.openSelfServiceOnce(worker.session)
	}()
	go func() {
		defer wg.Done()
		_, confirmed = e.decideOnce(worker.session, pending.result.PendingID, "confirm")
	}()
	wg.Wait()

	if entryResponse.status != http.StatusOK {
		t.Fatalf("page entry returned %d, want 200", entryResponse.status)
	}

	// Whichever way the race resolved, the terminal must see exactly one final
	// state and the ledger exactly one consumption for the second meal.
	_, status := e.terminalPending(terminalCredential, pending.result.PendingID)
	if status.Status == "PENDING" {
		t.Fatal("the raced request is still waiting; the server must decide one final state")
	}
	balance := e.employeeBalance(worker.session)
	if status.Code == "CANCELLED" {
		// Cancel won: the scan confirmation could not charge, so only the first
		// scan is a consumption.
		if confirmed.Status == "SUCCESS" {
			t.Fatal("the scan confirmation charged after the request was cancelled")
		}
		if consumers := len(e.transactions(worker.id, "CONSUME")); consumers != 1 {
			t.Fatalf("cancellation winning left %d consumptions, want the first scan only", consumers)
		}
		if balance != 20000-mealPrice {
			t.Fatalf("balance is %d with the cancellation winning, want %d", balance, 20000-mealPrice)
		}
		if cancelPreview.Status != "READY" || cancelPreview.ExistingCount != 1 {
			t.Fatalf("preview after the cancellation is %v, want READY with 1 prior consumption", cancelPreview)
		}
	} else {
		// The scan won: the cancellation must not reverse the charge, so both the
		// first and the confirmed scan are consumptions.
		if confirmed.Status != "SUCCESS" {
			t.Fatalf("the scan confirmation ended as %s/%s, want SUCCESS", confirmed.Status, confirmed.Code)
		}
		if consumers := len(e.transactions(worker.id, "CONSUME")); consumers != 2 {
			t.Fatalf("the scan winning left %d consumptions, want two", consumers)
		}
		if cancelPreview.Status != "READY" || cancelPreview.ExistingCount != 2 {
			t.Fatalf("preview after the scan won is %v, want READY with 2 prior consumptions", cancelPreview)
		}
		if balance != 20000-2*mealPrice {
			t.Fatalf("balance is %d with the scan winning, want %d", balance, 20000-2*mealPrice)
		}
	}
}

// --- story 25: retries versus a new consumption -----------------------------

// TestSelfServiceRetryDoesNotBecomeANewConsumption covers story 25: re-reading
// the result and retrying the same confirmation never creates a second charge,
// while an explicit new preview does.
func TestSelfServiceRetryDoesNotBecomeANewConsumption(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("C013", "13800000513", "戚十三")
	e.recharge(worker.id, 20000, "R-C013")

	preview := e.openSelfService(worker.session)
	_, first := e.confirmSelfService(worker.session, preview.IntentID)
	if first.Status != "SUCCESS" || first.Consumption == nil {
		t.Fatalf("first confirmation returned %s/%s, want SUCCESS", first.Status, first.Code)
	}

	for attempt := 0; attempt < 3; attempt++ {
		if _, retried := e.confirmSelfService(worker.session, preview.IntentID); retried.Status != "SUCCESS" {
			t.Fatalf("retry %d returned %s/%s, want the original SUCCESS", attempt, retried.Status, retried.Code)
		}
		if _, read := e.selfServiceResult(worker.session, preview.IntentID); read.Status != "SUCCESS" {
			t.Fatalf("result read %d returned %s/%s, want SUCCESS", attempt, read.Status, read.Code)
		}
	}

	if consumers := len(e.transactions(worker.id, "CONSUME")); consumers != 1 {
		t.Fatalf("retries produced %d consumptions, want exactly 1", consumers)
	}

	// Only an explicit new preview starts the next consumption.
	next := e.openSelfService(worker.session)
	if next.IntentID == preview.IntentID {
		t.Fatal("the new preview reused the consumed intent")
	}
	_, second := e.confirmSelfService(worker.session, next.IntentID)
	if second.Status != "SUCCESS" {
		t.Fatalf("the explicitly requested second consumption returned %s/%s, want SUCCESS", second.Status, second.Code)
	}
	if consumers := len(e.transactions(worker.id, "CONSUME")); consumers != 2 {
		t.Fatalf("employee has %d consumptions, want 2", consumers)
	}
}
