// Acceptance coverage for CW-35 (#40): stable fund history and transaction detail.
package acceptance

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"
)

type adminTransactionsPage struct {
	Items []struct {
		ID                   int64  `json:"id"`
		TransactionNo        string `json:"transaction_no"`
		EmployeeID           int64  `json:"employee_id"`
		EmployeeNo           string `json:"employee_no"`
		EmployeeName         string `json:"employee_name"`
		Department           string `json:"department"`
		Type                 string `json:"type"`
		RelatedTransactionID int64  `json:"related_transaction_id"`
		RefundStatus         string `json:"refund_status"`
		CanRefund            bool   `json:"can_refund"`
		RefundBlockReason    string `json:"refund_block_reason"`
		ReversalStatus       string `json:"reversal_status"`
		CanReverse           bool   `json:"can_reverse"`
		ReversalBlockReason  string `json:"reversal_block_reason"`
		PayoutStatus         string `json:"payout_status"`
		WithdrawalID         *int64 `json:"withdrawal_transaction_id"`
	} `json:"items"`
	NextCursor string `json:"next_cursor"`
}

type adminTransactionDetail struct {
	Transaction struct {
		ID                   int64  `json:"id"`
		EmployeeID           int64  `json:"employee_id"`
		EmployeeNo           string `json:"employee_no"`
		Type                 string `json:"type"`
		RelatedTransactionID int64  `json:"related_transaction_id"`
		CurrentBalance       int64  `json:"current_balance_cents"`
		AccountStatus        string `json:"account_status"`
		EmployeeStatus       string `json:"employee_status"`
		PayoutStatus         string `json:"payout_status"`
		WithdrawalID         *int64 `json:"withdrawal_transaction_id"`
		MealSnapshot         *struct {
			Source       string  `json:"source"`
			MealCode     string  `json:"meal_code"`
			MealName     *string `json:"meal_name"`
			BusinessDate string  `json:"business_date"`
			AmountCents  int64   `json:"amount_cents"`
		} `json:"meal_snapshot"`
		RechargeReceipt *struct {
			ID            int64  `json:"id"`
			ReceiptRef    string `json:"receipt_ref"`
			AmountCents   int64  `json:"amount_cents"`
			CollectedAt   string `json:"collected_at"`
			PaymentMethod string `json:"payment_method"`
		} `json:"recharge_receipt"`
		EnteredBy *struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"entered_by"`
		Related *struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"related_transaction"`
		RefundStatus        string `json:"refund_status"`
		CanRefund           bool   `json:"can_refund"`
		RefundBlockReason   string `json:"refund_block_reason"`
		ReversalStatus      string `json:"reversal_status"`
		CanReverse          bool   `json:"can_reverse"`
		ReversalBlockReason string `json:"reversal_block_reason"`
	} `json:"transaction"`
}

func TestAdminTransactionsStablePagesIdentityActionsAndDetail(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	first := e.newEmployee("LEDGER-001", "13800000601", "流水甲")
	second := e.newEmployee("LEDGER-002", "13800000602", "流水乙")

	var recharge struct {
		Transaction struct {
			ID int64 `json:"id"`
		} `json:"transaction"`
	}
	e.post(e.public.URL, "/api/admin/recharges", e.adminToken, map[string]any{
		"employee_id": first.id, "amount_cents": 10000, "receipt_ref": "LEDGER-R1",
		"collected_at": time.Now().UTC().Format(time.RFC3339Nano), "payment_method": "CASH", "idempotency_key": "ledger-recharge-1",
	}).expect(t, http.StatusCreated, &recharge)
	firstRechargeID := recharge.Transaction.ID
	preview := e.openSelfService(first.session)
	_, outcome := e.confirmSelfService(first.session, preview.IntentID)
	if outcome.Status != "SUCCESS" || outcome.Consumption == nil {
		t.Fatalf("create consumption through employee flow: %+v", outcome)
	}
	consumeID := outcome.Consumption.TransactionID
	e.recharge(second.id, 4000, "LEDGER-R2")

	var firstPage adminTransactionsPage
	e.get(e.public.URL, "/api/admin/transactions?limit=2", e.adminToken).expect(t, http.StatusOK, &firstPage)
	if len(firstPage.Items) != 2 || firstPage.NextCursor == "" || firstPage.Items[0].EmployeeID != second.id || firstPage.Items[1].EmployeeID != first.id {
		t.Fatalf("unexpected first stable page: %+v", firstPage)
	}
	var secondPage adminTransactionsPage
	e.get(e.public.URL, "/api/admin/transactions?limit=2&cursor="+url.QueryEscape(firstPage.NextCursor), e.adminToken).
		expect(t, http.StatusOK, &secondPage)
	if len(secondPage.Items) != 1 || secondPage.NextCursor != "" || secondPage.Items[0].ID != firstRechargeID {
		t.Fatalf("cursor did not return exactly the final older row: %+v", secondPage)
	}
	var filtered adminTransactionsPage
	e.get(e.public.URL, fmt.Sprintf("/api/admin/transactions?employee_id=%d&type=CONSUME", first.id), e.adminToken).
		expect(t, http.StatusOK, &filtered)
	if len(filtered.Items) != 1 || filtered.Items[0].ID != consumeID || filtered.Items[0].EmployeeNo != "LEDGER-001" || filtered.Items[0].EmployeeName != "流水甲" {
		t.Fatalf("employee/type filter omitted identity or returned wrong row: %+v", filtered)
	}
	if !filtered.Items[0].CanRefund || filtered.Items[0].RefundStatus != "AVAILABLE" || filtered.Items[0].RefundBlockReason != "" {
		t.Fatalf("unrefunded consumption action state is wrong: %+v", filtered.Items[0])
	}

	e.refund(consumeID)
	e.post(e.public.URL, fmt.Sprintf("/api/admin/recharges/%d/reverse", firstRechargeID), e.adminToken, map[string]string{
		"idempotency_key": "ledger-reverse-1", "reason": "验收冲正",
	}).expect(t, http.StatusCreated, nil)

	var consumeDetail adminTransactionDetail
	e.get(e.public.URL, fmt.Sprintf("/api/admin/transactions/%d", consumeID), e.adminToken).expect(t, http.StatusOK, &consumeDetail)
	if consumeDetail.Transaction.EmployeeID != first.id || consumeDetail.Transaction.Type != "CONSUME" ||
		consumeDetail.Transaction.RelatedTransactionID != 0 || consumeDetail.Transaction.CanRefund ||
		consumeDetail.Transaction.RefundStatus != "REFUNDED" || consumeDetail.Transaction.RefundBlockReason != "already_refunded" {
		t.Fatalf("refunded consumption detail has wrong identity/action state: %+v", consumeDetail.Transaction)
	}
	var reversalDetail adminTransactionDetail
	e.get(e.public.URL, fmt.Sprintf("/api/admin/transactions/%d", firstRechargeID), e.adminToken).expect(t, http.StatusOK, &reversalDetail)
	if reversalDetail.Transaction.EmployeeID != first.id || reversalDetail.Transaction.Type != "RECHARGE" ||
		reversalDetail.Transaction.CanReverse || reversalDetail.Transaction.ReversalStatus != "REVERSED" ||
		reversalDetail.Transaction.ReversalBlockReason != "already_reversed" {
		t.Fatalf("reversed recharge detail has wrong identity/action state: %+v", reversalDetail.Transaction)
	}
	var reversal adminTransactionsPage
	e.get(e.public.URL, "/api/admin/transactions?type=RECHARGE_REVERSAL", e.adminToken).expect(t, http.StatusOK, &reversal)
	if len(reversal.Items) != 1 || reversal.Items[0].RelatedTransactionID != firstRechargeID {
		t.Fatalf("reversal list row does not link to original recharge: %+v", reversal)
	}
	var reversalTransactionDetail adminTransactionDetail
	e.get(e.public.URL, fmt.Sprintf("/api/admin/transactions/%d", reversal.Items[0].ID), e.adminToken).
		expect(t, http.StatusOK, &reversalTransactionDetail)
	if reversalTransactionDetail.Transaction.Related == nil || reversalTransactionDetail.Transaction.Related.ID != firstRechargeID ||
		reversalTransactionDetail.Transaction.Related.Type != "RECHARGE" {
		t.Fatalf("reversal detail did not return its validated original recharge: %+v", reversalTransactionDetail.Transaction)
	}
	var missing apiResponse
	missing = e.get(e.public.URL, "/api/admin/transactions/999999", e.adminToken)
	missing.expect(t, http.StatusNotFound, nil)
	e.get(e.public.URL, "/api/admin/transactions/not-an-id", e.adminToken).expect(t, http.StatusNotFound, nil)
	e.get(e.public.URL, "/api/admin/transactions?cursor=abc", e.adminToken).expect(t, http.StatusBadRequest, nil)
	e.get(e.public.URL, "/api/admin/transactions?employee_id=abc", e.adminToken).expect(t, http.StatusBadRequest, nil)
	e.get(e.public.URL, "/api/admin/transactions?limit=2", "").expect(t, http.StatusUnauthorized, nil)
}

func TestAdminTransactionDetailIncludesPersistedBusinessSnapshotsAndPayoutState(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
	worker := e.newEmployee("DETAIL-001", "13800000611", "明细甲")

	var recharge struct {
		Transaction struct {
			ID int64 `json:"id"`
		} `json:"transaction"`
	}
	e.post(e.public.URL, "/api/admin/recharges", e.adminToken, map[string]any{
		"employee_id": worker.id, "amount_cents": 5000, "receipt_ref": "DETAIL-R1",
		"collected_at": "2026-09-30T01:02:03Z", "payment_method": "BANK_TRANSFER", "idempotency_key": "detail-recharge-1",
	}).expect(t, http.StatusCreated, &recharge)
	preview := e.openSelfService(worker.session)
	_, outcome := e.confirmSelfService(worker.session, preview.IntentID)
	if outcome.Status != "SUCCESS" || outcome.Consumption == nil {
		t.Fatalf("create self-service consumption: %+v", outcome)
	}
	consumeID := outcome.Consumption.TransactionID

	// Change the live meal name after charging. Detail must still use the
	// transaction's immutable snapshot instead of current meal configuration.
	if _, err := e.db.ExecContext(t.Context(), `UPDATE meal_periods SET name='已修改的当前名称' WHERE code=?`, defaultMealCode); err != nil {
		t.Fatalf("mutate current meal config fixture: %v", err)
	}
	accountID := e.accountID(worker.id)
	e.post(e.public.URL, fmt.Sprintf("/api/admin/accounts/%d/withdraw", accountID), e.adminToken, map[string]any{
		"payout_ref": "DETAIL-CLOSE", "paid_at": "2026-09-30T01:10:00Z", "payment_method": "CASH",
		"idempotency_key": "detail-close-1", "related_refund_transaction_id": 0,
	}).expect(t, http.StatusCreated, nil)

	var consumeDetail adminTransactionDetail
	e.get(e.public.URL, fmt.Sprintf("/api/admin/transactions/%d", consumeID), e.adminToken).expect(t, http.StatusOK, &consumeDetail)
	if consumeDetail.Transaction.CurrentBalance != 0 || consumeDetail.Transaction.AccountStatus != "CLOSED" || consumeDetail.Transaction.EmployeeStatus != "CLOSED" {
		t.Fatalf("detail did not report latest account/employee state: %+v", consumeDetail.Transaction)
	}
	snapshot := consumeDetail.Transaction.MealSnapshot
	if snapshot == nil || snapshot.Source != "SELF_SERVICE" || snapshot.MealCode != defaultMealCode ||
		snapshot.MealName == nil || *snapshot.MealName != defaultMealName || snapshot.BusinessDate != preview.BusinessDate || snapshot.AmountCents != mealPrice {
		t.Fatalf("detail did not report persisted consumption snapshot: %+v", snapshot)
	}
	if consumeDetail.Transaction.RechargeReceipt != nil || consumeDetail.Transaction.EnteredBy != nil {
		t.Fatalf("self-service consumption unexpectedly has an offline receipt or admin author: %+v", consumeDetail.Transaction)
	}
	if _, err := e.db.ExecContext(t.Context(), `UPDATE meal_periods SET name=? WHERE code=?`, defaultMealName, defaultMealCode); err != nil {
		t.Fatalf("restore meal config fixture: %v", err)
	}

	e.refund(consumeID)
	var refundDetail adminTransactionDetail
	var refundID int64
	if err := e.db.QueryRowContext(t.Context(), `SELECT id FROM transactions WHERE type='REFUND' AND related_transaction_id=?`, consumeID).Scan(&refundID); err != nil {
		t.Fatalf("find refund transaction fixture: %v", err)
	}
	e.get(e.public.URL, fmt.Sprintf("/api/admin/transactions/%d", refundID), e.adminToken).expect(t, http.StatusOK, &refundDetail)
	if refundDetail.Transaction.AccountStatus != "CLOSED" || refundDetail.Transaction.CurrentBalance != mealPrice ||
		refundDetail.Transaction.PayoutStatus != "AVAILABLE" || refundDetail.Transaction.WithdrawalID != nil {
		t.Fatalf("unpaid closed-account refund should be available for payout: %+v", refundDetail.Transaction)
	}
	e.post(e.public.URL, fmt.Sprintf("/api/admin/accounts/%d/withdraw", accountID), e.adminToken, map[string]any{
		"payout_ref": "DETAIL-REFUND-PAYOUT", "paid_at": "2026-09-30T01:15:00Z", "payment_method": "CASH",
		"idempotency_key": "detail-refund-payout-1", "related_refund_transaction_id": refundID,
	}).expect(t, http.StatusCreated, nil)
	e.get(e.public.URL, fmt.Sprintf("/api/admin/transactions/%d", refundID), e.adminToken).expect(t, http.StatusOK, &refundDetail)
	if refundDetail.Transaction.PayoutStatus != "PAID" || refundDetail.Transaction.WithdrawalID == nil || *refundDetail.Transaction.WithdrawalID < 1 {
		t.Fatalf("paid refund detail omitted payout linkage: %+v", refundDetail.Transaction)
	}

	var rechargeDetail adminTransactionDetail
	e.get(e.public.URL, fmt.Sprintf("/api/admin/transactions/%d", recharge.Transaction.ID), e.adminToken).expect(t, http.StatusOK, &rechargeDetail)
	receipt := rechargeDetail.Transaction.RechargeReceipt
	if receipt == nil || receipt.ReceiptRef != "DETAIL-R1" || receipt.AmountCents != 5000 || receipt.CollectedAt != "2026-09-30T01:02:03Z" || receipt.PaymentMethod != "BANK_TRANSFER" {
		t.Fatalf("recharge detail omitted its offline receipt: %+v", receipt)
	}
	if rechargeDetail.Transaction.EnteredBy == nil || rechargeDetail.Transaction.EnteredBy.Username != adminUsername ||
		rechargeDetail.Transaction.EnteredBy.ID < 1 {
		t.Fatalf("recharge detail omitted readable administrator identity: %+v", rechargeDetail.Transaction.EnteredBy)
	}
	if rechargeDetail.Transaction.AccountStatus != "CLOSED" || rechargeDetail.Transaction.EmployeeStatus != "CLOSED" || rechargeDetail.Transaction.CurrentBalance != 0 {
		t.Fatalf("recharge detail did not report current account state: %+v", rechargeDetail.Transaction)
	}
	e.get(e.public.URL, "/api/admin/transactions/999999", e.adminToken).expect(t, http.StatusNotFound, nil)
	e.get(e.public.URL, "/api/admin/transactions/0", e.adminToken).expect(t, http.StatusNotFound, nil)
	e.get(e.public.URL, "/api/admin/transactions/999999", "").expect(t, http.StatusUnauthorized, nil)
}

func TestAdminManualSupplyDetailUsesItsStoredSnapshotWithoutMealNameInference(t *testing.T) {
	e := newEnv(t)
	worker := e.newEmployee("DETAIL-MANUAL", "13800000612", "明细乙")
	e.recharge(worker.id, 3000, "DETAIL-MANUAL-R")
	var registered struct {
		ID int64 `json:"id"`
	}
	e.post(e.public.URL, "/api/admin/manual-supplies", e.adminToken, map[string]any{
		"receipt_ref": "DETAIL-MANUAL-SUPPLY", "employee_id": worker.id, "meal_code": "BREAKFAST",
		"business_date": "2026-08-04", "amount_cents": 1200, "note": "历史手工补录",
	}).expect(t, http.StatusCreated, &registered)
	e.post(e.public.URL, fmt.Sprintf("/api/admin/manual-supplies/%d/post", registered.ID), e.adminToken, map[string]string{
		"idempotency_key": "detail-manual-post-1",
	}).expect(t, http.StatusCreated, nil)
	var transactionID int64
	if err := e.db.QueryRowContext(t.Context(), `SELECT transaction_id FROM manual_supplies WHERE id=?`, registered.ID).Scan(&transactionID); err != nil {
		t.Fatalf("read manual supply transaction fixture: %v", err)
	}
	var detail adminTransactionDetail
	e.get(e.public.URL, fmt.Sprintf("/api/admin/transactions/%d", transactionID), e.adminToken).expect(t, http.StatusOK, &detail)
	snapshot := detail.Transaction.MealSnapshot
	if snapshot == nil || snapshot.Source != "MANUAL_SUPPLY" || snapshot.MealCode != "BREAKFAST" ||
		snapshot.MealName != nil || snapshot.BusinessDate != "2026-08-04" || snapshot.AmountCents != 1200 {
		t.Fatalf("manual supply detail should use its stored fields and leave uncaptured name empty: %+v", snapshot)
	}
}

func TestAdminRefundListShowsClosedAccountPayoutAvailabilityAndPaymentLink(t *testing.T) {
	e := newEnv(t)
	worker := e.newEmployee("PAYOUT-LIST", "13800000613", "流水丙")
	e.recharge(worker.id, 5000, "PAYOUT-LIST-R")
	createManualConsumption := func(receiptRef, businessDate, key string) int64 {
		t.Helper()
		var registered struct {
			ID int64 `json:"id"`
		}
		e.post(e.public.URL, "/api/admin/manual-supplies", e.adminToken, map[string]any{
			"receipt_ref": receiptRef, "employee_id": worker.id, "meal_code": "LUNCH",
			"business_date": businessDate, "amount_cents": 1200, "note": "退款流水验收",
		}).expect(t, http.StatusCreated, &registered)
		e.post(e.public.URL, fmt.Sprintf("/api/admin/manual-supplies/%d/post", registered.ID), e.adminToken, map[string]string{
			"idempotency_key": key,
		}).expect(t, http.StatusCreated, nil)
		var transactionID int64
		if err := e.db.QueryRowContext(t.Context(), `SELECT transaction_id FROM manual_supplies WHERE id=?`, registered.ID).Scan(&transactionID); err != nil {
			t.Fatalf("read manual supply transaction: %v", err)
		}
		return transactionID
	}
	firstConsume := createManualConsumption("PAYOUT-LIST-S1", "2026-08-01", "payout-list-consume-1")
	secondConsume := createManualConsumption("PAYOUT-LIST-S2", "2026-08-02", "payout-list-consume-2")
	accountID := e.accountID(worker.id)
	e.post(e.public.URL, fmt.Sprintf("/api/admin/accounts/%d/withdraw", accountID), e.adminToken, map[string]any{
		"payout_ref": "PAYOUT-LIST-CLOSE", "paid_at": "2026-09-30T02:00:00Z", "payment_method": "CASH",
		"idempotency_key": "payout-list-close", "related_refund_transaction_id": 0,
	}).expect(t, http.StatusCreated, nil)
	e.refund(firstConsume)
	e.refund(secondConsume)
	refundIDs := map[int64]int64{}
	for _, consumeID := range []int64{firstConsume, secondConsume} {
		var refundID int64
		if err := e.db.QueryRowContext(t.Context(), `SELECT id FROM transactions WHERE type='REFUND' AND related_transaction_id=?`, consumeID).Scan(&refundID); err != nil {
			t.Fatalf("find refund transaction: %v", err)
		}
		refundIDs[consumeID] = refundID
	}
	paidRefundID := refundIDs[firstConsume]
	e.post(e.public.URL, fmt.Sprintf("/api/admin/accounts/%d/withdraw", accountID), e.adminToken, map[string]any{
		"payout_ref": "PAYOUT-LIST-REFUND-1", "paid_at": "2026-09-30T02:10:00Z", "payment_method": "CASH",
		"idempotency_key": "payout-list-refund-1", "related_refund_transaction_id": paidRefundID,
	}).expect(t, http.StatusCreated, nil)

	var page adminTransactionsPage
	e.get(e.public.URL, fmt.Sprintf("/api/admin/transactions?employee_id=%d&type=REFUND", worker.id), e.adminToken).
		expect(t, http.StatusOK, &page)
	if len(page.Items) != 2 {
		t.Fatalf("refund list returned %d rows, want 2: %+v", len(page.Items), page.Items)
	}
	got := make(map[int64]struct {
		status string
		id     *int64
	})
	for _, item := range page.Items {
		got[item.ID] = struct {
			status string
			id     *int64
		}{item.PayoutStatus, item.WithdrawalID}
	}
	paid := got[paidRefundID]
	unpaid := got[refundIDs[secondConsume]]
	if paid.status != "PAID" || paid.id == nil || *paid.id < 1 {
		t.Fatalf("paid refund list row omitted its payout transaction id: %+v", paid)
	}
	if unpaid.status != "AVAILABLE" || unpaid.id != nil {
		t.Fatalf("unpaid closed-account refund list row should remain available: %+v", unpaid)
	}
}

func TestAdminRefundSettledBeforeAccountClosureCannotBePaidOutAgain(t *testing.T) {
	e := newEnv(t)
	worker := e.newEmployee("PRE-CLOSE-REFUND", "13800000614", "关户前退款")
	e.recharge(worker.id, 5000, "PRE-CLOSE-REFUND-R")
	e.postManualSupply(worker.id, "PRE-CLOSE-REFUND-S", "2026-09-29", 1200)
	var consumeID int64
	if err := e.db.QueryRowContext(t.Context(), `SELECT transaction_id FROM manual_supplies WHERE receipt_ref=?`, "PRE-CLOSE-REFUND-S").Scan(&consumeID); err != nil {
		t.Fatalf("find manual consumption: %v", err)
	}
	e.refund(consumeID)
	var refundID int64
	if err := e.db.QueryRowContext(t.Context(), `SELECT id FROM transactions WHERE type='REFUND' AND related_transaction_id=?`, consumeID).Scan(&refundID); err != nil {
		t.Fatalf("find refund transaction: %v", err)
	}

	accountID := e.accountID(worker.id)
	var closingPayout struct {
		Transaction struct {
			ID int64 `json:"id"`
		} `json:"transaction"`
	}
	e.post(e.public.URL, fmt.Sprintf("/api/admin/accounts/%d/withdraw", accountID), e.adminToken, map[string]any{
		"payout_ref": "PRE-CLOSE-REFUND-CLOSE", "paid_at": "2026-09-30T03:00:00Z", "payment_method": "CASH",
		"idempotency_key": "pre-close-refund-close", "related_refund_transaction_id": 0,
	}).expect(t, http.StatusCreated, &closingPayout)

	var page adminTransactionsPage
	e.get(e.public.URL, fmt.Sprintf("/api/admin/transactions?employee_id=%d&type=REFUND", worker.id), e.adminToken).
		expect(t, http.StatusOK, &page)
	if len(page.Items) != 1 || page.Items[0].ID != refundID || page.Items[0].PayoutStatus != "PAID" ||
		page.Items[0].WithdrawalID == nil || *page.Items[0].WithdrawalID != closingPayout.Transaction.ID {
		t.Fatalf("refund settled through full-balance account closure should not remain payable: %+v", page.Items)
	}
	var detail adminTransactionDetail
	e.get(e.public.URL, fmt.Sprintf("/api/admin/transactions/%d", refundID), e.adminToken).expect(t, http.StatusOK, &detail)
	if detail.Transaction.PayoutStatus != "PAID" || detail.Transaction.WithdrawalID == nil ||
		*detail.Transaction.WithdrawalID != closingPayout.Transaction.ID {
		t.Fatalf("refund detail omitted the account-closing payout: %+v", detail.Transaction)
	}

	duplicate := e.post(e.public.URL, fmt.Sprintf("/api/admin/accounts/%d/withdraw", accountID), e.adminToken, map[string]any{
		"payout_ref": "PRE-CLOSE-REFUND-DUPLICATE", "paid_at": "2026-09-30T03:10:00Z", "payment_method": "CASH",
		"idempotency_key": "pre-close-refund-duplicate", "related_refund_transaction_id": refundID,
	})
	duplicate.expect(t, http.StatusConflict, nil)
}
