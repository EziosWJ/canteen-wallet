package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

// SPEC-003 user stories 1-34 (#22).
//
// The fixed self-service link is a shared, identity-free entry point. Opening it
// is an explicit server operation that cancels the employee's own waiting scan
// requests and returns a preview decided entirely by the server. Confirming
// charges the employee's stored-value account once per intent. Assertions go
// through the public employee API and the internal terminal API and observe
// balances, fund transactions, consumption details and final states only.

// selfServicePreview mirrors the preview payload returned by opening the page.
type selfServicePreview struct {
	Status        string `json:"status"`
	Code          string `json:"code"`
	Message       string `json:"message"`
	MealCode      string `json:"meal_code"`
	MealName      string `json:"meal_name"`
	BusinessDate  string `json:"business_date"`
	AmountCents   int64  `json:"amount_cents"`
	ExistingCount int    `json:"existing_count"`
	IntentID      string `json:"intent_id"`
}

// selfServiceOutcome mirrors the confirm/result payload.
type selfServiceOutcome struct {
	Status      string              `json:"status"`
	Code        string              `json:"code"`
	Message     string              `json:"message"`
	Consumption *scanResult         `json:"consumption"`
	Preview     *selfServicePreview `json:"preview"`
}

// openSelfService performs the page-entry operation for a session.
func (e *env) openSelfService(session string) selfServicePreview {
	e.t.Helper()
	var preview selfServicePreview
	e.post(e.public.URL, "/api/me/self-service", session, nil).expect(e.t, http.StatusOK, &preview)
	return preview
}

func (e *env) openSelfServiceOnce(session string) (apiResponse, selfServicePreview) {
	var preview selfServicePreview
	response, err := e.postOnce(e.public.URL, "/api/me/self-service", session, nil)
	if err != nil {
		return apiResponse{}, selfServicePreview{}
	}
	if response.status == http.StatusOK {
		if err := json.Unmarshal(response.body, &preview); err != nil {
			return response, selfServicePreview{}
		}
	}
	return response, preview
}

func (e *env) confirmSelfService(session, intentID string) (apiResponse, selfServiceOutcome) {
	e.t.Helper()
	response := e.post(e.public.URL, "/api/me/self-service/"+intentID+"/confirm", session, nil)
	var outcome selfServiceOutcome
	if response.status == http.StatusOK {
		if err := json.Unmarshal(response.body, &outcome); err != nil {
			e.t.Fatalf("decode self-service outcome %s: %v", response.body, err)
		}
	}
	return response, outcome
}

func (e *env) confirmSelfServiceOnce(session, intentID string) (apiResponse, selfServiceOutcome) {
	response, err := e.postOnce(e.public.URL, "/api/me/self-service/"+intentID+"/confirm", session, nil)
	if err != nil {
		return apiResponse{}, selfServiceOutcome{}
	}
	var outcome selfServiceOutcome
	if response.status == http.StatusOK {
		if err := json.Unmarshal(response.body, &outcome); err != nil {
			return response, selfServiceOutcome{}
		}
	}
	return response, outcome
}

func (e *env) selfServiceResult(session, intentID string) (apiResponse, selfServiceOutcome) {
	e.t.Helper()
	response := e.get(e.public.URL, "/api/me/self-service/"+intentID+"/result", session)
	var outcome selfServiceOutcome
	if response.status == http.StatusOK {
		if err := json.Unmarshal(response.body, &outcome); err != nil {
			e.t.Fatalf("decode self-service result %s: %v", response.body, err)
		}
	}
	return response, outcome
}

// --- stories 5, 6, 7, 12, 13: the preview ----------------------------------

// TestSelfServicePreviewShowsServerDecidedMealAndPrice covers stories 5, 6 and
// 13: the page shows the server's current meal and price, and merely opening the
// link charges nothing.
func TestSelfServicePreviewShowsServerDecidedMealAndPrice(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("S001", "13800000401", "林一")
	e.recharge(worker.id, 5000, "R-S001")
	before := e.employeeBalance(worker.session)

	preview := e.openSelfService(worker.session)

	if preview.Status != "READY" {
		t.Fatalf("preview status is %s/%s, want READY", preview.Status, preview.Code)
	}
	if preview.MealCode != defaultMealCode || preview.MealName != defaultMealName {
		t.Fatalf("preview reported %s/%s, want %s/%s",
			preview.MealCode, preview.MealName, defaultMealCode, defaultMealName)
	}
	if preview.AmountCents != mealPrice {
		t.Fatalf("preview amount is %d, want %d", preview.AmountCents, mealPrice)
	}
	if preview.ExistingCount != 0 {
		t.Fatalf("first preview reports %d existing consumptions, want 0", preview.ExistingCount)
	}
	if preview.IntentID == "" {
		t.Fatal("preview returned no confirmation credential")
	}
	if preview.BusinessDate != time.Now().In(e.location).Format("2006-01-02") {
		t.Fatalf("preview business date is %q, want today in the canteen time zone", preview.BusinessDate)
	}

	// Stories 13 and 16: viewing never charges.
	if balance := e.employeeBalance(worker.session); balance != before {
		t.Fatalf("balance is %d after viewing the preview, want %d", balance, before)
	}
	if entries := e.transactions(worker.id, "CONSUME"); len(entries) != 0 {
		t.Fatalf("viewing the preview wrote %d consumptions, want 0", len(entries))
	}

	// Retrying the entry is idempotent and does not charge either.
	retried := e.openSelfService(worker.session)
	if retried.IntentID != preview.IntentID {
		t.Fatalf("retrying entry produced a new intent %q, want %q", retried.IntentID, preview.IntentID)
	}
	if balance := e.employeeBalance(worker.session); balance != before {
		t.Fatalf("balance is %d after retrying entry, want %d", balance, before)
	}
}

// TestSelfServicePreviewRejectsWhenNoMealIsActive covers story 12: outside every
// meal window the page explains why and hands out no confirmation credential, so
// no attempt to confirm can charge.
func TestSelfServicePreviewRejectsWhenNoMealIsActive(t *testing.T) {
	e := newEnv(t)
	e.disableAllMeals()
	worker := e.newEmployee("S002", "13800000402", "韩二")
	e.recharge(worker.id, 5000, "R-S002")

	preview := e.openSelfService(worker.session)

	if preview.Status != "UNAVAILABLE" {
		t.Fatalf("preview status is %s, want UNAVAILABLE when no meal is active", preview.Status)
	}
	if preview.Code != "NO_ACTIVE_PERIOD" {
		t.Fatalf("preview code is %s, want NO_ACTIVE_PERIOD", preview.Code)
	}
	if preview.IntentID != "" {
		t.Fatalf("unavailable preview returned credential %q, want none", preview.IntentID)
	}
	if preview.Message == "" {
		t.Fatal("unavailable preview gave no reason for the employee")
	}
	if balance := e.employeeBalance(worker.session); balance != 5000 {
		t.Fatalf("balance is %d without an active meal, want 5000", balance)
	}
}

// --- stories 1, 2, 3, 4, 34: the fixed link itself --------------------------

// TestSelfServiceRequiresLoginAndReturnsToTheSamePage covers stories 2 and 4:
// the shared link carries no identity, an unauthenticated call is refused, and
// the credentials only ever act on the calling employee's own account.
func TestSelfServiceRequiresLoginAndReturnsToTheSamePage(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("S003", "13800000403", "赵三")
	e.recharge(worker.id, 5000, "R-S003")

	// Unauthenticated entry is rejected before any state changes.
	unauthenticated := e.post(e.public.URL, "/api/me/self-service", "", nil)
	if unauthenticated.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated entry returned %d, want 401", unauthenticated.status)
	}

	preview := e.openSelfService(worker.session)
	_, outcome := e.confirmSelfService(worker.session, preview.IntentID)
	if outcome.Status != "SUCCESS" {
		t.Fatalf("confirmation returned %s/%s, want SUCCESS", outcome.Status, outcome.Code)
	}
	if spent := e.employeeBalance(worker.session); spent != 5000-mealPrice {
		t.Fatalf("balance is %d, want %d", spent, 5000-mealPrice)
	}
}

// TestSelfServiceRequiresPasswordChangeFirst covers story 3: the forced
// temporary-password change still gates the fixed link, exactly as it gates
// issuing a payment code.
func TestSelfServiceRequiresPasswordChangeFirst(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)

	var created struct {
		Employee struct {
			ID int64 `json:"id"`
		} `json:"employee"`
		TemporaryPassword string `json:"temporary_password"`
	}
	e.post(e.public.URL, "/api/admin/employees", e.adminToken, map[string]string{
		"employee_no": "S004", "name": "钱四", "phone": "13800000404", "department": "研发",
	}).expect(t, http.StatusCreated, &created)
	var login struct {
		AccessToken string `json:"access_token"`
	}
	e.post(e.public.URL, "/api/auth/login", "", map[string]string{
		"phone": "13800000404", "password": created.TemporaryPassword,
	}).expect(t, http.StatusOK, &login)

	blocked := e.post(e.public.URL, "/api/me/self-service", login.AccessToken, nil)
	if blocked.status != http.StatusForbidden {
		t.Fatalf("entry before the password change returned %d, want 403", blocked.status)
	}
}

// --- story 27, 28, 30: page entry cancels this employee's own scan requests --

// TestSelfServiceEntryCancelsOwnScanRequest covers stories 27 and 30: opening
// the page finishes the employee's waiting scan request with a final cancelled
// state that the terminal can observe, so the terminal stops waiting.
func TestSelfServiceEntryCancelsOwnScanRequest(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("S005", "13800000405", "孙五")
	e.recharge(worker.id, 5000, "R-S005")
	terminalCredential := e.terminal("terminal-ss-1", "自助取消终端")

	pending := e.scanPending(t, worker, terminalCredential)
	charged := 5000 - mealPrice

	preview := e.openSelfService(worker.session)
	if preview.Status != "READY" {
		t.Fatalf("preview status is %s, want READY", preview.Status)
	}

	// The terminal must observe the final cancelled state by the original id.
	response, status := e.terminalPending(terminalCredential, pending.result.PendingID)
	if response.status != http.StatusOK {
		t.Fatalf("terminal pending lookup returned %d, want 200", response.status)
	}
	if status.Status != "FAILED" || status.Code != "CANCELLED" {
		t.Fatalf("pending request ended as %s/%s, want FAILED/CANCELLED", status.Status, status.Code)
	}

	// The scan request is gone, so confirming it is impossible; balance is
	// unchanged by the cancellation itself.
	if balance := e.employeeBalance(worker.session); balance != charged {
		t.Fatalf("balance is %d after auto-cancel, want %d", balance, charged)
	}
	if _, outcome := e.confirmSelfService(worker.session, preview.IntentID); outcome.Status != "SUCCESS" {
		t.Fatalf("confirmation after auto-cancel returned %s/%s, want SUCCESS", outcome.Status, outcome.Code)
	}
	if balance := e.employeeBalance(worker.session); balance != charged-mealPrice {
		t.Fatalf("balance is %d, want %d", balance, charged-mealPrice)
	}
	if consumers := len(e.transactions(worker.id, "CONSUME")); consumers != 2 {
		t.Fatalf("employee has %d consumptions after self-service, want 2", consumers)
	}
}

// TestSelfServiceEntryKeepsOtherEmployeesRequests covers story 34: cancelling my
// own waiting scan requests must not touch another employee's.
func TestSelfServiceEntryKeepsOtherEmployeesRequests(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	first := e.newEmployee("S006", "13800000406", "李六")
	second := e.newEmployee("S007", "13800000407", "周六")
	e.recharge(first.id, 5000, "R-S006")
	e.recharge(second.id, 5000, "R-S007")
	terminalCredential := e.terminal("terminal-ss-2", "自助隔离终端")

	otherPending := e.scanPending(t, second, terminalCredential)

	e.openSelfService(first.session)

	// The other employee's request must still be waiting.
	_, status := e.terminalPending(terminalCredential, otherPending.result.PendingID)
	if status.Status != "PENDING" {
		t.Fatalf("other employee's request became %s/%s, want PENDING", status.Status, status.Code)
	}
	// And the other employee can still settle it themselves.
	_, decided := e.decide(second.session, otherPending.result.PendingID, "confirm")
	if decided.Status != "SUCCESS" {
		t.Fatalf("other employee's confirmation returned %s/%s, want SUCCESS", decided.Status, decided.Code)
	}
}

// TestSelfServiceEntryKeepsCompletedScanConsumption covers story 28: a scan that
// already charged is not reversed by opening the self-service page, and it still
// counts as an existing consumption.
func TestSelfServiceEntryKeepsCompletedScanConsumption(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("S008", "13800000408", "吴八")
	e.recharge(worker.id, 5000, "R-S008")
	terminalCredential := e.terminal("terminal-ss-3", "自助保留终端")

	token := e.issue(worker.session, "")
	if scan := e.scan(terminalCredential, token.Token); scan.Status != "SUCCESS" {
		t.Fatalf("first scan returned %s/%s, want SUCCESS", scan.Status, scan.Code)
	}

	preview := e.openSelfService(worker.session)

	if preview.Status != "READY" {
		t.Fatalf("preview status is %s, want READY", preview.Status)
	}
	if preview.ExistingCount != 1 {
		t.Fatalf("preview reports %d existing consumptions, want 1", preview.ExistingCount)
	}
	if balance := e.employeeBalance(worker.session); balance != 5000-mealPrice {
		t.Fatalf("balance is %d, want the completed scan kept (%d)", balance, 5000-mealPrice)
	}
	if consumers := len(e.transactions(worker.id, "CONSUME")); consumers != 1 {
		t.Fatalf("employee has %d consumptions, want the completed scan only", consumers)
	}
}

// --- stories 14, 23, 31, 32, 33: confirming one self-service consumption -----

// TestSelfServiceConfirmChargesOnceAndShowsDetails covers stories 14, 23, 31, 32
// and 33: the confirmation charges exactly once, labels its own source, keeps
// the meal snapshot and reports the consumption number derived from the ledger.
func TestSelfServiceConfirmChargesOnceAndShowsDetails(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("S009", "13800000409", "郑九")
	e.recharge(worker.id, 5000, "R-S009")

	preview := e.openSelfService(worker.session)
	_, outcome := e.confirmSelfService(worker.session, preview.IntentID)

	if outcome.Status != "SUCCESS" {
		t.Fatalf("confirmation returned %s/%s, want SUCCESS", outcome.Status, outcome.Code)
	}
	if outcome.Consumption == nil {
		t.Fatal("successful confirmation returned no consumption details")
	}
	result := *outcome.Consumption
	if result.MealCode != defaultMealCode || result.MealName != defaultMealName {
		t.Fatalf("consumption reported %s/%s, want %s/%s",
			result.MealCode, result.MealName, defaultMealCode, defaultMealName)
	}
	if result.AmountCents != mealPrice {
		t.Fatalf("consumption amount is %d, want %d", result.AmountCents, mealPrice)
	}
	if result.OccurredAt == "" {
		t.Fatal("consumption reported no time")
	}
	if result.ConsumptionNo != consumptionNumber(result.TransactionID) {
		t.Fatalf("consumption number is %q, want the ledger id (%q)",
			result.ConsumptionNo, consumptionNumber(result.TransactionID))
	}
	if result.EmployeeName != worker.name {
		t.Fatalf("consumption reported employee %q, want %q", result.EmployeeName, worker.name)
	}

	if balance := e.employeeBalance(worker.session); balance != 5000-mealPrice {
		t.Fatalf("balance is %d, want %d", balance, 5000-mealPrice)
	}

	entries := e.transactions(worker.id, "CONSUME")
	if len(entries) != 1 {
		t.Fatalf("employee has %d consumptions, want exactly 1", len(entries))
	}
	entry := entries[0]
	if entry.BusinessType != "SELF_SERVICE" {
		t.Fatalf("consumption source is %q, want SELF_SERVICE", entry.BusinessType)
	}
	if entry.TerminalID != "" {
		t.Fatalf("self-service consumption carries terminal %q, want none", entry.TerminalID)
	}
	if entry.AdministratorID != 0 {
		t.Fatalf("self-service consumption carries administrator %d, want none", entry.AdministratorID)
	}
	if entry.Amount != -mealPrice {
		t.Fatalf("ledger amount is %d, want %d", entry.Amount, -mealPrice)
	}
	if entry.ID != result.TransactionID {
		t.Fatalf("consumption transaction %d does not match ledger entry %d", result.TransactionID, entry.ID)
	}
	if entry.BusinessID != preview.BusinessDate+"|"+defaultMealCode {
		t.Fatalf("ledger business id is %q, want the business date and meal", entry.BusinessID)
	}

	// Story 32: the snapshot survives a later configuration change, so re-reading
	// the same consumption still reports the meal and price it was charged with.
	e.reconfigureMeal(defaultMealCode, "改名午餐", 2000)
	_, replayed := e.selfServiceResult(worker.session, preview.IntentID)
	if replayed.Status != "SUCCESS" || replayed.Consumption == nil {
		t.Fatalf("result after reconfiguration is %s, want the original SUCCESS", replayed.Status)
	}
	if replayed.Consumption.MealName != defaultMealName {
		t.Fatalf("stored snapshot meal name is %q, want the original %q",
			replayed.Consumption.MealName, defaultMealName)
	}
	if replayed.Consumption.AmountCents != mealPrice {
		t.Fatalf("stored snapshot amount is %d, want the original %d",
			replayed.Consumption.AmountCents, mealPrice)
	}
	if replayed.Consumption.TransactionID != result.TransactionID {
		t.Fatalf("stored snapshot points at transaction %d, want %d",
			replayed.Consumption.TransactionID, result.TransactionID)
	}
}

// TestSelfServiceResultSurvivesRefresh covers stories 24 and 22: re-reading the
// same intent returns the same consumption without charging again, and a second
// confirm is a replay rather than a new charge.
func TestSelfServiceResultSurvivesRefresh(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("S010", "13800000410", "冯十")
	e.recharge(worker.id, 5000, "R-S010")

	preview := e.openSelfService(worker.session)
	_, first := e.confirmSelfService(worker.session, preview.IntentID)
	if first.Status != "SUCCESS" {
		t.Fatalf("first confirmation returned %s/%s, want SUCCESS", first.Status, first.Code)
	}

	// Refreshing the success page asks the server again.
	_, refreshed := e.selfServiceResult(worker.session, preview.IntentID)
	if refreshed.Status != "SUCCESS" || refreshed.Consumption == nil {
		t.Fatalf("refreshed result is %s, want the same SUCCESS consumption", refreshed.Status)
	}
	if refreshed.Consumption.TransactionID != first.Consumption.TransactionID ||
		refreshed.Consumption.ConsumptionNo != first.Consumption.ConsumptionNo {
		t.Fatalf("refreshed result reports consumption %d/%s, want %d/%s",
			refreshed.Consumption.TransactionID, refreshed.Consumption.ConsumptionNo,
			first.Consumption.TransactionID, first.Consumption.ConsumptionNo)
	}

	// Retrying the confirmation is the same intent, not a second charge.
	_, retried := e.confirmSelfService(worker.session, preview.IntentID)
	if retried.Status != "SUCCESS" || retried.Consumption == nil {
		t.Fatalf("retried confirmation returned %s/%s, want the same SUCCESS", retried.Status, retried.Code)
	}
	if retried.Consumption.TransactionID != first.Consumption.TransactionID {
		t.Fatalf("retried confirmation charged transaction %d, want %d",
			retried.Consumption.TransactionID, first.Consumption.TransactionID)
	}

	if balance := e.employeeBalance(worker.session); balance != 5000-mealPrice {
		t.Fatalf("balance is %d after refresh and retry, want a single charge (%d)", balance, 5000-mealPrice)
	}
	if consumers := len(e.transactions(worker.id, "CONSUME")); consumers != 1 {
		t.Fatalf("employee has %d consumptions after retries, want exactly 1", consumers)
	}
}

// TestSelfServiceConcurrentConfirmsChargeOnce covers story 22: two tabs or a
// double tap on the same intent produce at most one consumption.
func TestSelfServiceConcurrentConfirmsChargeOnce(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("S011", "13800000411", "陈十一")
	e.recharge(worker.id, 5000, "R-S011")

	preview := e.openSelfService(worker.session)

	const racers = 4
	var wg sync.WaitGroup
	outcomes := make([]selfServiceOutcome, racers)
	statuses := make([]int, racers)
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func(index int) {
			defer wg.Done()
			response, outcome := e.confirmSelfServiceOnce(worker.session, preview.IntentID)
			statuses[index], outcomes[index] = response.status, outcome
		}(i)
	}
	wg.Wait()

	charged := 0
	transactionIDs := map[int64]bool{}
	for i, outcome := range outcomes {
		if statuses[i] != http.StatusOK {
			t.Fatalf("concurrent confirmation %d returned %d, want 200", i, statuses[i])
		}
		if outcome.Status == "SUCCESS" && outcome.Consumption != nil {
			charged++
			transactionIDs[outcome.Consumption.TransactionID] = true
		}
	}
	if charged != racers {
		t.Fatalf("%d of %d concurrent confirmations succeeded, want every racer to observe success", charged, racers)
	}
	if len(transactionIDs) != 1 {
		t.Fatalf("concurrent confirmations reported %d different consumptions, want 1", len(transactionIDs))
	}
	if balance := e.employeeBalance(worker.session); balance != 5000-mealPrice {
		t.Fatalf("balance is %d after concurrent confirms, want %d", balance, 5000-mealPrice)
	}
	if consumers := len(e.transactions(worker.id, "CONSUME")); consumers != 1 {
		t.Fatalf("employee has %d consumptions after concurrent confirms, want exactly 1", consumers)
	}
}

func consumptionNumber(transactionID int64) string {
	return "C" + fmt.Sprintf("%010d", transactionID)
}
