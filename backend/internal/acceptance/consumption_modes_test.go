// Acceptance coverage for CW-26 (#30): administrators choose which consumption
// entrances are open. Assertions observe HTTP status, mode persistence, and
// whether funds actually move.
package acceptance

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

// modesResponse mirrors the payload of both the public and the admin settings
// endpoints.
type modesResponse struct {
	PaymentCode bool `json:"payment_code"`
	SelfService bool `json:"self_service"`
}

// publicModes reads the modes an employee page sees.
func (e *env) publicModes() modesResponse {
	e.t.Helper()
	var modes modesResponse
	e.get(e.public.URL, "/api/consumption-modes", "").expect(e.t, http.StatusOK, &modes)
	return modes
}

// setModes writes the modes through the admin API.
func (e *env) setModes(token string, paymentCode, selfService bool) apiResponse {
	e.t.Helper()
	return e.put(e.public.URL, "/api/admin/settings/consumption-modes", token, map[string]bool{
		"payment_code": paymentCode, "self_service": selfService,
	})
}

// expectModes asserts that employees and administrators see the same modes.
func (e *env) expectModes(t *testing.T, token string, paymentCode, selfService bool) {
	t.Helper()
	var admin modesResponse
	e.get(e.public.URL, "/api/admin/settings/consumption-modes", token).expect(t, http.StatusOK, &admin)
	for name, got := range map[string]modesResponse{"public": e.publicModes(), "admin": admin} {
		if got.PaymentCode != paymentCode || got.SelfService != selfService {
			t.Fatalf("%s modes are %+v, want payment_code=%v self_service=%v", name, got, paymentCode, selfService)
		}
	}
}

// TestModesDefaultToBothEntrancesOnMigration covers the upgrade guarantee: an
// existing database keeps both entrances after migrating.
func TestModesDefaultToBothEntrancesOnMigration(t *testing.T) {
	e := newEnv(t)
	e.expectModes(t, e.adminToken, true, true)
}

// TestModesSupportEveryCombinationExceptAllDisabled covers the three valid
// configurations and the rejected fourth.
func TestModesSupportEveryCombinationExceptAllDisabled(t *testing.T) {
	e := newEnv(t)
	// The installed default is both entrances, so this walk starts by turning one
	// off: each step below is a real change and therefore a new audit record.
	combinations := []struct {
		name string
		want modesResponse
	}{
		{"code only", modesResponse{PaymentCode: true, SelfService: false}},
		{"self service only", modesResponse{PaymentCode: false, SelfService: true}},
		{"both", modesResponse{PaymentCode: true, SelfService: true}},
	}
	for index, combination := range combinations {
		e.setModes(e.adminToken, combination.want.PaymentCode, combination.want.SelfService).expect(t, http.StatusOK, nil)
		e.expectModes(t, e.adminToken, combination.want.PaymentCode, combination.want.SelfService)
		// Each accepted change records exactly one audit event.
		if audits := e.countAuditAction(e.adminToken, "CONSUMPTION_MODES_UPDATED"); audits != index+1 {
			t.Fatalf("%s left %d audit records, want %d", combination.name, audits, index+1)
		}
	}
	// Turning both entrances off is refused and leaves the modes untouched.
	e.setModes(e.adminToken, false, false).expect(t, http.StatusBadRequest, nil)
	e.expectModes(t, e.adminToken, true, true)
	// A write that changes nothing is not a change, so it records no audit.
	before := e.countAuditAction(e.adminToken, "CONSUMPTION_MODES_UPDATED")
	e.setModes(e.adminToken, true, true).expect(t, http.StatusOK, nil)
	if after := e.countAuditAction(e.adminToken, "CONSUMPTION_MODES_UPDATED"); after != before {
		t.Fatalf("an unchanged write recorded %d extra audit records", after-before)
	}
}

// TestModesSurviveARestart covers the durability requirement: the choice is
// stored, not held in memory.
func TestModesSurviveARestart(t *testing.T) {
	e := newEnv(t)
	e.setModes(e.adminToken, true, false).expect(t, http.StatusOK, nil)
	e.restartServers()
	e.expectModes(t, e.adminToken, true, false)
}

// TestDisabledPaymentCodeRefusesNewConsumption covers the server-side gate: a
// code is refused even though the employee page also hides the entrance.
func TestDisabledPaymentCodeRefusesNewConsumption(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("M001", "13800000401", "罗成")
	e.recharge(worker.id, 5000, "R-M001")
	e.setModes(e.adminToken, false, true).expect(t, http.StatusOK, nil)
	e.issueRaw(worker.session, "").expect(t, http.StatusConflict, nil)
	if balance := e.balance(worker.id); balance != 5000 {
		t.Fatalf("a refused code changed the balance to %d", balance)
	}
}

// TestDisabledSelfServiceRefusesPreviewAndConfirmation covers the same gate for
// the fixed link, including a confirmation of an intent minted before the change.
func TestDisabledSelfServiceRefusesPreviewAndConfirmation(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("M002", "13800000402", "沈墨")
	e.recharge(worker.id, 5000, "R-M002")
	preview := e.openSelfService(worker.session)
	if preview.Status != "READY" || preview.IntentID == "" {
		t.Fatalf("self-service preview is not ready: %+v", preview)
	}
	e.setModes(e.adminToken, true, false).expect(t, http.StatusOK, nil)
	// The page reports the entrance is closed instead of offering a confirmation.
	closed := e.openSelfService(worker.session)
	if closed.Status != "UNAVAILABLE" || closed.Code != "MODE_DISABLED" || closed.IntentID != "" {
		t.Fatalf("closed self-service entrance should be UNAVAILABLE/MODE_DISABLED: %+v", closed)
	}
	// Confirming the intent minted before the change charges nothing.
	_, outcome := e.confirmSelfService(worker.session, preview.IntentID)
	if outcome.Status == "SUCCESS" {
		t.Fatalf("a disabled entrance still charged: %+v", outcome)
	}
	if balance := e.balance(worker.id); balance != 5000 {
		t.Fatalf("a disabled entrance charged %d", 5000-balance)
	}
}

// TestDisabledEntranceLeavesCompletedConsumptionsQueryable covers the guarantee
// that history, results and refunds survive a mode change.
func TestDisabledEntranceLeavesCompletedConsumptionsQueryable(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("M003", "13800000403", "费无极")
	e.recharge(worker.id, 5000, "R-M003")
	preview := e.openSelfService(worker.session)
	response, outcome := e.confirmSelfService(worker.session, preview.IntentID)
	response.expect(t, http.StatusOK, nil)
	if outcome.Status != "SUCCESS" || outcome.Consumption == nil || outcome.Consumption.TransactionID == 0 {
		t.Fatalf("self-service consumption did not complete: %+v", outcome)
	}
	transactionID := outcome.Consumption.TransactionID

	e.setModes(e.adminToken, true, false).expect(t, http.StatusOK, nil)

	// The result page still resolves the same consumption.
	resultResponse, result := e.selfServiceResult(worker.session, preview.IntentID)
	resultResponse.expect(t, http.StatusOK, nil)
	if result.Status != "SUCCESS" || result.Consumption == nil || result.Consumption.TransactionID != transactionID {
		t.Fatalf("a completed consumption stopped resolving after the mode change: %+v", result)
	}
	// The administrator history still lists it.
	found := false
	for _, entry := range e.transactions(worker.id, "CONSUME") {
		if entry.ID == transactionID {
			found = true
		}
	}
	if !found {
		t.Fatalf("completed consumption %d disappeared from the history", transactionID)
	}
	// And it can still be refunded.
	e.refund(transactionID)
	if balance := e.balance(worker.id); balance != 5000 {
		t.Fatalf("refund of a consumption made before the mode change left balance %d", balance)
	}
}

// TestDisabledEntranceLeavesTheOtherOneWorking covers the independence of the
// two entrances.
func TestDisabledEntranceLeavesTheOtherOneWorking(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("M004", "13800000404", "顾青")
	e.recharge(worker.id, 5000, "R-M004")
	e.setModes(e.adminToken, false, true).expect(t, http.StatusOK, nil)
	preview := e.openSelfService(worker.session)
	if preview.Status != "READY" {
		t.Fatalf("the enabled entrance should keep working: %+v", preview)
	}
	_, outcome := e.confirmSelfService(worker.session, preview.IntentID)
	if outcome.Status != "SUCCESS" {
		t.Fatalf("the enabled entrance should keep working: %+v", outcome)
	}
}

// TestWaitingConfirmationCannotChargeAfterPaymentCodeIsDisabled covers the race
// requirement: a confirmation already waiting on the terminal must not charge
// once the entrance is off, and must not charge later either.
func TestWaitingConfirmationCannotChargeAfterPaymentCodeIsDisabled(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("M005", "13800000405", "裴照")
	e.recharge(worker.id, 5000, "R-M005")
	token := e.terminal("terminal-modes", "模式验收终端")

	pending := e.scanPending(t, worker, token)
	balanceBefore := e.balance(worker.id)

	e.setModes(e.adminToken, false, true).expect(t, http.StatusOK, nil)

	// The waiting confirmation is finished with a final failure, not left open.
	response, status := e.terminalPending(token, pending.result.PendingID)
	response.expect(t, http.StatusOK, nil)
	if status.Status != "FAILED" || status.Code != "MODE_DISABLED" {
		t.Fatalf("a disabled entrance left the confirmation as %+v", status)
	}
	if balance := e.balance(worker.id); balance != balanceBefore {
		t.Fatalf("a disabled entrance charged %d", balanceBefore-balance)
	}
	// Confirming the stale request afterwards must not charge either.
	e.decide(worker.session, pending.result.PendingID, "confirm")
	if balance := e.balance(worker.id); balance != balanceBefore {
		t.Fatalf("confirming a stale request charged %d", balanceBefore-e.balance(worker.id))
	}
}

// TestModeDisableRacingAConfirmationNeverChargesSilently covers the ordering
// requirement directly: the mode disable and the employee's confirmation are
// issued concurrently, and whichever order they land in, the balance and the
// response agree. A charge is only acceptable when the response reports SUCCESS
// with that transaction; a refusal must leave the balance untouched.
func TestModeDisableRacingAConfirmationNeverChargesSilently(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("M006", "13800000406", "柳襄")
	e.recharge(worker.id, 5000, "R-M006")

	pending := e.scanPending(t, worker, e.terminal("terminal-modes-race", "模式竞争终端"))
	balanceBefore := e.balance(worker.id)

	var wait sync.WaitGroup
	wait.Add(2)
	var decisionResponse apiResponse
	var decisionOutcome scanResult
	var modeResponse apiResponse
	var modeErr error
	go func() {
		defer wait.Done()
		decisionResponse, decisionOutcome = e.decideOnce(worker.session, pending.result.PendingID, "confirm")
	}()
	go func() {
		defer wait.Done()
		modeResponse, modeErr = e.putOnce(e.public.URL, "/api/admin/settings/consumption-modes", e.adminToken,
			map[string]bool{"payment_code": false, "self_service": true})
	}()
	wait.Wait()

	if modeErr != nil {
		t.Fatalf("mode change failed: %v", modeErr)
	}
	if modeResponse.status != http.StatusOK {
		t.Fatalf("mode change returned %d: %s", modeResponse.status, modeResponse.body)
	}
	if decisionResponse.status != http.StatusOK {
		t.Fatalf("confirmation returned %d: %s", decisionResponse.status, decisionResponse.body)
	}

	balanceAfter := e.balance(worker.id)
	charged := balanceBefore - balanceAfter
	switch {
	case decisionOutcome.Status == "SUCCESS":
		// The confirmation won the race, so exactly one meal must have been
		// charged and it must be the transaction the response named.
		if charged != mealPrice {
			t.Fatalf("a successful confirmation charged %d, want %d", charged, mealPrice)
		}
		if decisionOutcome.TransactionID == 0 {
			t.Fatal("a successful confirmation reported no transaction")
		}
		found := false
		for _, entry := range e.transactions(worker.id, "CONSUME") {
			if entry.ID == decisionOutcome.TransactionID {
				found = true
			}
		}
		if !found {
			t.Fatalf("charged transaction %d is missing from the ledger", decisionOutcome.TransactionID)
		}
	case charged != 0:
		// The disable won the race, so the confirmation must not have moved money.
		t.Fatalf("a refused confirmation (status %s/%s) still charged %d",
			decisionOutcome.Status, decisionOutcome.Code, charged)
	}
	// The entrance is closed afterwards in every ordering.
	e.expectModes(t, e.adminToken, false, true)
}

// TestReEnabledEntranceRequiresAFreshFlow covers story 13: after an entrance is
// switched back on, the request that died while it was off must not be revivable;
// the employee starts again and gets a new code and a new confirmation.
func TestReEnabledEntranceRequiresAFreshFlow(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("M007", "13800000407", "周叙")
	e.recharge(worker.id, 5000, "R-M007")
	token := e.terminal("terminal-modes-reenable", "重开验收终端")

	pending := e.scanPending(t, worker, token)
	balanceBefore := e.balance(worker.id)

	e.setModes(e.adminToken, false, true).expect(t, http.StatusOK, nil)
	// The waiting scan is closed while the entrance is off.
	_, closed := e.terminalPending(token, pending.result.PendingID)
	if closed.Status != "FAILED" || closed.Code != "MODE_DISABLED" {
		t.Fatalf("a disabled entrance left the confirmation as %+v", closed)
	}
	e.setModes(e.adminToken, true, true).expect(t, http.StatusOK, nil)

	// Switching the entrance back on does not resurrect the dead request.
	_, revived := e.terminalPending(token, pending.result.PendingID)
	if revived.Status != "FAILED" || revived.Code != "MODE_DISABLED" {
		t.Fatalf("re-enabling revived a finished request: %+v", revived)
	}
	e.decide(worker.session, pending.result.PendingID, "confirm")
	if balance := e.balance(worker.id); balance != balanceBefore {
		t.Fatalf("a request closed by the mode change charged %d after re-enabling", balanceBefore-balance)
	}

	// A fresh flow works again. Step outside the 3-second dedup window first, so
	// the scan is judged on its own rather than merged into the closed one.
	e.ageFirstScanEvents(10 * time.Second)
	fresh := e.issue(worker.session, newPresentationID(t))
	scan := e.scan(token, fresh.Token)
	if scan.Status != "PENDING" {
		t.Fatalf("a re-enabled entrance did not raise a fresh confirmation: %+v", scan)
	}
	if scan.PendingID == pending.result.PendingID {
		t.Fatal("the fresh scan reused the confirmation the mode change had closed")
	}
	_, outcome := e.decide(worker.session, scan.PendingID, "confirm")
	if outcome.Status != "SUCCESS" {
		t.Fatalf("confirming the fresh request failed: %+v", outcome)
	}
	if balance := e.balance(worker.id); balance != balanceBefore-mealPrice {
		t.Fatalf("the fresh flow left balance %d, want %d", balance, balanceBefore-mealPrice)
	}
}

// TestModeDisableRacingASelfServiceConfirmationNeverChargesSilently covers the
// same ordering guarantee for the other entrance: the spec names both the scan
// confirmation and the self-service preview/confirmation. The disable and the
// confirmation are issued concurrently, and whichever order they land in the
// balance and the response agree.
func TestModeDisableRacingASelfServiceConfirmationNeverChargesSilently(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("M008", "13800000408", "谢临")
	e.recharge(worker.id, 5000, "R-M008")

	preview := e.openSelfService(worker.session)
	if preview.Status != "READY" || preview.IntentID == "" {
		t.Fatalf("self-service preview is not ready: %+v", preview)
	}
	balanceBefore := e.balance(worker.id)

	var wait sync.WaitGroup
	wait.Add(2)
	var confirmResponse apiResponse
	var confirmOutcome selfServiceOutcome
	var modeResponse apiResponse
	var modeErr error
	go func() {
		defer wait.Done()
		confirmResponse, confirmOutcome = e.confirmSelfServiceOnce(worker.session, preview.IntentID)
	}()
	go func() {
		defer wait.Done()
		modeResponse, modeErr = e.putOnce(e.public.URL, "/api/admin/settings/consumption-modes", e.adminToken,
			map[string]bool{"payment_code": true, "self_service": false})
	}()
	wait.Wait()

	if modeErr != nil {
		t.Fatalf("mode change failed: %v", modeErr)
	}
	if modeResponse.status != http.StatusOK {
		t.Fatalf("mode change returned %d: %s", modeResponse.status, modeResponse.body)
	}

	charged := balanceBefore - e.balance(worker.id)
	switch {
	case confirmResponse.status == http.StatusOK && confirmOutcome.Status == "SUCCESS":
		// The confirmation won the race, so it must have charged exactly one meal
		// and named the transaction it charged.
		if confirmOutcome.Consumption == nil || confirmOutcome.Consumption.TransactionID == 0 {
			t.Fatalf("a successful confirmation reported no transaction: %+v", confirmOutcome)
		}
		if charged != mealPrice {
			t.Fatalf("a successful confirmation charged %d, want %d", charged, mealPrice)
		}
	case charged != 0:
		// The disable won the race, so the confirmation must not have moved money.
		t.Fatalf("a refused confirmation (http %d, status %s) still charged %d",
			confirmResponse.status, confirmOutcome.Status, charged)
	}
	// The entrance is closed afterwards in every ordering.
	e.expectModes(t, e.adminToken, true, false)
}
