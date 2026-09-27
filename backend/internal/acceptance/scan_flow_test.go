package acceptance

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

// SPEC-002 user stories 26, 27, 32, 33, 34.
//
// The server opens one fixed three-second repeat window per employee, business
// date and meal, timed from the scan that opened it. Every further scan inside
// that window is a repeat: it returns the opening result, is recorded as an
// event, and moves no money. The window is never extended by the repeats it
// merges.
//
// Two repeat signs exist and both mean "this scan repeats an earlier one":
// rescanning the same code is a replay of that code, while a different code for
// the same employee, date and meal is a duplicate trigger of the window.

// TestSameCodeReplayAlwaysReturnsFirstResult covers story 27: after a code has
// been used successfully, any rescan of that same code reports the original
// consumption and never charges twice.
func TestSameCodeReplayAlwaysReturnsFirstResult(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("C001", "13800000201", "白宇")
	e.recharge(worker.id, 5000, "R-C001")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	issued := e.issue(worker.session, "")
	first := e.scan(terminalCredential, issued.Token)
	if first.Status != "SUCCESS" {
		t.Fatalf("first scan returned %s/%s, want SUCCESS", first.Status, first.Code)
	}
	if first.Replayed || first.DuplicateTrigger {
		t.Fatalf("the first scan was classified as a repeat: %+v", first)
	}

	// The repeat sign depends on which rule catches the scan: inside the window
	// the same code is both a duplicate trigger and a replay, while a later
	// rescan is caught by the per-code replay rule alone. Both mean the same
	// thing to the terminal.
	inside := e.scan(terminalCredential, issued.Token)
	if !inside.Replayed || !inside.DuplicateTrigger {
		t.Fatalf("rescan inside the window reported replay=%t duplicate=%t, want both",
			inside.Replayed, inside.DuplicateTrigger)
	}

	// Once the window has closed, rescanning the same code is caught by the
	// per-code replay rule alone, at any later time.
	for _, wait := range []time.Duration{3 * time.Second, 5 * time.Second, time.Minute} {
		e.ageFirstScanEvents(wait)
		replay := e.scan(terminalCredential, issued.Token)
		if replay.Status != "SUCCESS" || replay.Code != "CONSUMED" {
			t.Fatalf("replay after %s returned %s/%s, want SUCCESS/CONSUMED", wait, replay.Status, replay.Code)
		}
		if !replay.Replayed || replay.DuplicateTrigger {
			t.Fatalf("replay after %s reported replay=%t duplicate=%t, want a plain replay",
				wait, replay.Replayed, replay.DuplicateTrigger)
		}
		if replay.TransactionID != first.TransactionID {
			t.Fatalf("replay after %s reported transaction %d, want %d",
				wait, replay.TransactionID, first.TransactionID)
		}
		if replay.ConsumptionNo != first.ConsumptionNo || replay.AmountCents != first.AmountCents {
			t.Fatalf("replay after %s changed the detail: %+v vs %+v", wait, replay, first)
		}
		if replay.OccurredAt != first.OccurredAt {
			t.Fatalf("replay after %s reported charge time %s, want the original %s",
				wait, replay.OccurredAt, first.OccurredAt)
		}
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 1 {
		t.Fatalf("replays produced %d consumption flows, want 1", len(flows))
	}
	if balance := e.employeeBalance(worker.session); balance != 5000-mealPrice {
		t.Fatalf("balance is %d after replays, want %d", balance, 5000-mealPrice)
	}
}

// TestRepeatInsideWindowMergesIntoFirstResult covers story 26: a device that
// fires again inside the window, with another code and from another terminal, is
// told the first result and the event is recorded as a repeat rather than a
// second charge.
func TestRepeatInsideWindowMergesIntoFirstResult(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("C002", "13800000202", "从珊")
	e.recharge(worker.id, 5000, "R-C002")
	firstTerminal := e.terminal("terminal-1", "一号终端")
	secondTerminal := e.terminal("terminal-2", "二号终端")

	issued := e.issue(worker.session, "")
	first := e.scan(firstTerminal, issued.Token)
	if first.Status != "SUCCESS" {
		t.Fatalf("first scan returned %s/%s, want SUCCESS", first.Status, first.Code)
	}
	if first.Replayed || first.DuplicateTrigger {
		t.Fatalf("the first scan was classified as a repeat: %+v", first)
	}

	// Same code, rescanning on the other terminal: a replay of that code.
	sameCode := e.scan(secondTerminal, issued.Token)
	if sameCode.Status != "SUCCESS" || !sameCode.Replayed {
		t.Fatalf("rescanning the same code returned %s/%s replay=%t, want the replayed success",
			sameCode.Status, sameCode.Code, sameCode.Replayed)
	}
	if sameCode.TransactionID != first.TransactionID {
		t.Fatalf("rescan reported transaction %d, want the first %d", sameCode.TransactionID, first.TransactionID)
	}

	// A sibling code of the same flow is settled by the flow's own success before
	// the window is consulted, so it comes back as a plain replay.
	sibling := e.injectToken(worker.session, issued.PresentationID, time.Now().UTC().Add(-31*time.Second), 60*time.Second)
	siblingResult := e.scan(secondTerminal, sibling)
	if siblingResult.TransactionID != first.TransactionID || !siblingResult.Replayed {
		t.Fatalf("a sibling code returned transaction %d replay=%t, want the first %d replayed",
			siblingResult.TransactionID, siblingResult.Replayed, first.TransactionID)
	}
	if siblingResult.DuplicateTrigger {
		t.Fatalf("a sibling code was reported as a duplicate trigger: %+v", siblingResult)
	}

	// Another code, the first code of a fresh flow, scanned on the other terminal
	// while the window is still open: a duplicate trigger of the first scan.
	other := e.issue(worker.session, newPresentationID(t))
	merged := e.scan(secondTerminal, other.Token)
	if merged.Status != "SUCCESS" || merged.Code != "CONSUMED" {
		t.Fatalf("cross-code repeat returned %s/%s, want SUCCESS/CONSUMED", merged.Status, merged.Code)
	}
	if !merged.DuplicateTrigger {
		t.Fatal("another code inside the window was not marked as a duplicate trigger")
	}
	if merged.TransactionID != first.TransactionID {
		t.Fatalf("cross-code repeat reported transaction %d, want the first %d",
			merged.TransactionID, first.TransactionID)
	}

	// The four scans are all recorded, and the money moved once. Event details are
	// asserted in TestDuplicateTriggerLeavesNoExtraFundsFlow.
	if events := e.scanEvents(); len(events) != 4 {
		t.Fatalf("%d scan events were recorded, want 4", len(events))
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 1 {
		t.Fatalf("repeats produced %d consumption flows, want 1", len(flows))
	}
	if balance := e.employeeBalance(worker.session); balance != 5000-mealPrice {
		t.Fatalf("balance is %d after repeats, want %d", balance, 5000-mealPrice)
	}
}

// TestDuplicateTriggerLeavesNoExtraFundsFlow covers story 32: the administrative
// trace of device repeats exists while the money movement stays single, and
// every repeat is attributed to the scan that opened the window.
func TestDuplicateTriggerLeavesNoExtraFundsFlow(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("C003", "13800000203", "慕青")
	e.recharge(worker.id, 5000, "R-C003")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	issued := e.issue(worker.session, "")
	origin := e.scan(terminalCredential, issued.Token)
	if origin.Status != "SUCCESS" {
		t.Fatalf("first scan returned %s/%s, want SUCCESS", origin.Status, origin.Code)
	}

	// Three more codes, each the first code of a fresh flow, all scanned while
	// the window opened by the first scan is still open.
	const repeats = 3
	for repeat := 0; repeat < repeats; repeat++ {
		flow := e.issue(worker.session, newPresentationID(t))
		result := e.scan(terminalCredential, flow.Token)
		if !result.DuplicateTrigger {
			t.Fatalf("repeat %d was not marked as a duplicate trigger: %+v", repeat, result)
		}
		if result.TransactionID != origin.TransactionID {
			t.Fatalf("repeat %d reported transaction %d, want the origin %d",
				repeat, result.TransactionID, origin.TransactionID)
		}
	}

	flows := e.transactions(worker.id, "CONSUME")
	if len(flows) != 1 {
		t.Fatalf("device repeats produced %d consumption flows, want 1", len(flows))
	}
	if flows[0].Amount != -mealPrice || flows[0].BalanceAfter != 5000-mealPrice {
		t.Fatalf("the single flow is %+v, want one charge of %d leaving %d",
			flows[0], -mealPrice, 5000-mealPrice)
	}

	// Every scan is on record: one origin carrying the consumption and one
	// duplicate per repeat, all pointing at the same transaction.
	events := e.scanEvents()
	if len(events) != repeats+1 {
		t.Fatalf("%d scan events were recorded, want %d", len(events), repeats+1)
	}
	duplicates, origins := 0, 0
	for _, event := range events {
		switch event.ResultCode {
		case "DUPLICATE_TRIGGER":
			duplicates++
		case "CONSUMED":
			origins++
		}
		if event.Transaction != 0 && event.Transaction != flows[0].ID {
			t.Fatalf("scan event %d references transaction %d, want only %d",
				event.ID, event.Transaction, flows[0].ID)
		}
	}
	if duplicates != repeats || origins != 1 {
		t.Fatalf("events are %d duplicates and %d origins, want %d and 1: %+v",
			duplicates, origins, repeats, events)
	}
}

// TestWindowIsNotExtendedByRepeats covers the fixed-window rule: repeats are
// measured from the scan that opened the window, so the meal becomes chargeable
// again three seconds after that scan, not three seconds after the last repeat.
func TestWindowIsNotExtendedByRepeats(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("C004", "13800000204", "段誉")
	e.recharge(worker.id, 5000, "R-C004")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	issued := e.issue(worker.session, "")
	origin := e.scan(terminalCredential, issued.Token)
	if origin.Status != "SUCCESS" {
		t.Fatalf("first scan returned %s/%s, want SUCCESS", origin.Status, origin.Code)
	}

	// Two seconds after the origin, another flow's first code is still inside the
	// window and is merged.
	e.ageFirstScanEvents(2 * time.Second)
	late := e.issue(worker.session, newPresentationID(t))
	lateResult := e.scan(terminalCredential, late.Token)
	if !lateResult.DuplicateTrigger {
		t.Fatalf("a repeat two seconds after the origin was not merged: %+v", lateResult)
	}
	if lateResult.TransactionID != origin.TransactionID {
		t.Fatalf("the repeat reported transaction %d, want the origin %d",
			lateResult.TransactionID, origin.TransactionID)
	}

	// Only two more seconds pass, so the origin's window is over even though the
	// repeat above happened one second ago. A new flow is no longer merged.
	e.ageFirstScanEvents(2 * time.Second)
	next := e.issue(worker.session, newPresentationID(t))
	second := e.scan(terminalCredential, next.Token)
	if second.Status != "PENDING" {
		t.Fatalf("post-window scan returned %s/%s, want a PENDING confirmation", second.Status, second.Code)
	}
	if second.TransactionID != 0 {
		t.Fatalf("a pending confirmation already carries transaction %d, want none", second.TransactionID)
	}

	// The original consumption is untouched, only one charge exists, and the
	// original code still replays it.
	replay := e.scan(terminalCredential, issued.Token)
	if replay.TransactionID != origin.TransactionID || replay.OccurredAt != origin.OccurredAt {
		t.Fatalf("the original code now reports %+v, want the original %+v", replay, origin)
	}
	if flows := e.transactions(worker.id, "CONSUME"); len(flows) != 1 {
		t.Fatalf("%d consumption flows exist before confirmation, want 1", len(flows))
	}
	if balance := e.employeeBalance(worker.session); balance != 5000-mealPrice {
		t.Fatalf("balance is %d before confirmation, want %d", balance, 5000-mealPrice)
	}
}

// TestSecondConsumptionAfterWindowNeedsNewCode covers the allowed path: more than
// three seconds after the first scan, a new code for the same meal can be
// consumed normally when the previous consumption was refunded.
func TestSecondConsumptionAfterWindowNeedsNewCode(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("C005", "13800000205", "程颖")
	e.recharge(worker.id, 5000, "R-C005")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	issued := e.issue(worker.session, "")
	first := e.scan(terminalCredential, issued.Token)
	if first.Status != "SUCCESS" {
		t.Fatalf("first scan returned %s/%s, want SUCCESS", first.Status, first.Code)
	}

	// The consumption is refunded in full, so the meal is free again.
	e.refund(first.TransactionID)

	// A new code after the window is charged directly, with no confirmation.
	e.ageFirstScanEvents(5 * time.Second)
	second := e.issue(worker.session, newPresentationID(t))
	result := e.scan(terminalCredential, second.Token)
	if result.Status != "SUCCESS" {
		t.Fatalf("second consumption returned %s/%s, want SUCCESS", result.Status, result.Code)
	}
	if result.TransactionID == first.TransactionID {
		t.Fatal("the second consumption reused the refunded transaction")
	}
	flows := e.transactions(worker.id, "CONSUME")
	if len(flows) != 2 {
		t.Fatalf("%d consumption flows exist, want 2: %+v", len(flows), flows)
	}
	refunds := e.transactions(worker.id, "REFUND")
	if len(refunds) != 1 || refunds[0].Amount != mealPrice {
		t.Fatalf("refunds are %+v, want one refund of %d", refunds, mealPrice)
	}
	if balance := e.employeeBalance(worker.session); balance != 5000-mealPrice {
		t.Fatalf("balance is %d, want %d", balance, 5000-mealPrice)
	}
}

// TestConsumptionSnapshotSurvivesConfigChange covers story 33: a past
// consumption keeps the meal name, code, amount and charge time that applied
// when it happened, even after the administrator edits the meal configuration.
func TestConsumptionSnapshotSurvivesConfigChange(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("C006", "13800000206", "卫青")
	e.recharge(worker.id, 5000, "R-C006")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	issued := e.issue(worker.session, "")
	first := e.scan(terminalCredential, issued.Token)
	if first.Status != "SUCCESS" {
		t.Fatalf("scan returned %s/%s, want SUCCESS", first.Status, first.Code)
	}
	if first.MealName != defaultMealName || first.AmountCents != mealPrice {
		t.Fatalf("scan reported %s/%d, want %s/%d", first.MealName, first.AmountCents, defaultMealName, mealPrice)
	}

	// The administrator renames the meal and raises its price afterwards.
	const renamed = "午餐（新）"
	const newPrice = int64(2000)
	e.reconfigureMeal(defaultMealCode, renamed, newPrice)

	// The recorded consumption still describes the original charge, both on a
	// replay and in the employee's own view of the flow.
	replay := e.scan(terminalCredential, issued.Token)
	if replay.MealName != defaultMealName || replay.AmountCents != mealPrice {
		t.Fatalf("replay after reconfiguration reported %s/%d, want the original %s/%d",
			replay.MealName, replay.AmountCents, defaultMealName, mealPrice)
	}
	if replay.ConsumptionNo != first.ConsumptionNo || replay.OccurredAt != first.OccurredAt {
		t.Fatalf("replay after reconfiguration changed the charge: %+v vs %+v", replay, first)
	}
	status := e.presentation(worker.session)
	if status.Result.MealName != defaultMealName || status.Result.AmountCents != mealPrice {
		t.Fatalf("employee view after reconfiguration reports %s/%d, want the original %s/%d",
			status.Result.MealName, status.Result.AmountCents, defaultMealName, mealPrice)
	}
	if balance := e.employeeBalance(worker.session); balance != 5000-mealPrice {
		t.Fatalf("balance is %d, want %d", balance, 5000-mealPrice)
	}

	// A consumption taken after the change uses the new configuration.
	e.refund(first.TransactionID)
	e.ageFirstScanEvents(5 * time.Second)
	later := e.issue(worker.session, newPresentationID(t))
	after := e.scan(terminalCredential, later.Token)
	if after.Status != "SUCCESS" || after.AmountCents != newPrice || after.MealName != renamed {
		t.Fatalf("consumption after the change reported %s %s/%d, want SUCCESS %s/%d",
			after.Status, after.MealName, after.AmountCents, renamed, newPrice)
	}
	if earlier := e.scan(terminalCredential, issued.Token); earlier.AmountCents != mealPrice || earlier.MealName != defaultMealName {
		t.Fatalf("the earlier consumption now reports %s/%d, want the unchanged %s/%d",
			earlier.MealName, earlier.AmountCents, defaultMealName, mealPrice)
	}
}

// TestConcurrentScansHaveOneFinalState covers story 34: simultaneous scans of the
// same code produce exactly one consumption and let every caller observe it.
func TestConcurrentScansHaveOneFinalState(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("C007", "13800000207", "常宁")
	e.recharge(worker.id, 5000, "R-C007")
	terminalCredential := e.terminal("terminal-1", "一号终端")

	issued := e.issue(worker.session, "")

	const scanners = 6
	var wait sync.WaitGroup
	results := make([]scanResult, scanners)
	statuses := make([]apiResponse, scanners)
	for index := 0; index < scanners; index++ {
		wait.Add(1)
		go func(slot int) {
			defer wait.Done()
			statuses[slot], results[slot] = e.scanOnce(terminalCredential, issued.Token)
		}(index)
	}
	wait.Wait()
	for slot, status := range statuses {
		if status.status != http.StatusOK {
			t.Fatalf("concurrent scan %d returned %d: %s", slot, status.status, status.body)
		}
	}

	// Every caller learns the same single consumption.
	var transaction int64
	for slot, result := range results {
		if result.Status != "SUCCESS" {
			t.Fatalf("concurrent scan %d returned %s/%s, want SUCCESS", slot, result.Status, result.Code)
		}
		if transaction == 0 {
			transaction = result.TransactionID
		}
		if result.TransactionID != transaction {
			t.Fatalf("concurrent scan %d reported transaction %d, want %d",
				slot, result.TransactionID, transaction)
		}
		if result.ConsumptionNo == "" || result.OccurredAt == "" {
			t.Fatalf("concurrent scan %d reported an incomplete result: %+v", slot, result)
		}
	}

	// Exactly one origin event was recorded, and the money moved once.
	events := e.scanEvents()
	origins := 0
	for _, event := range events {
		if event.ResultCode != "DUPLICATE_TRIGGER" && event.ResultCode != "TOKEN_REPLAY" {
			origins++
		}
	}
	if origins != 1 {
		t.Fatalf("%d origin scan events were recorded, want 1: %+v", origins, events)
	}
	flows := e.transactions(worker.id, "CONSUME")
	if len(flows) != 1 || flows[0].ID != transaction {
		t.Fatalf("consumption flows are %+v, want exactly flow %d", flows, transaction)
	}
	if balance := e.employeeBalance(worker.session); balance != 5000-mealPrice {
		t.Fatalf("balance is %d, want %d", balance, 5000-mealPrice)
	}
	if status := e.presentation(worker.session); status.State != "SUCCESS" ||
		status.Result.TransactionID != transaction {
		t.Fatalf("employee view reports %s/%d, want SUCCESS/%d",
			status.State, status.Result.TransactionID, transaction)
	}
}
