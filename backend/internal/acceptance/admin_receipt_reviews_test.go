// Acceptance coverage for CW-40 (#45): searchable, complete, stable receipt review pages.
package acceptance

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"
)

type receiptReviewPage struct {
	Items []struct {
		TransactionID         int64  `json:"transaction_id"`
		TransactionNo         string `json:"transaction_no"`
		Type                  string `json:"type"`
		AmountCents           int64  `json:"amount_cents"`
		EnteredBy             int64  `json:"entered_by"`
		Status                string `json:"status"`
		ReviewerID            int64  `json:"reviewer_id"`
		Note                  string `json:"note"`
		EmployeeID            int64  `json:"employee_id"`
		EmployeeNo            string `json:"employee_no"`
		EmployeeName          string `json:"employee_name"`
		Department            string `json:"department"`
		ReceiptID             int64  `json:"receipt_id"`
		ReceiptRef            string `json:"receipt_ref"`
		CollectedAt           string `json:"collected_at"`
		PaymentMethod         string `json:"payment_method"`
		RechargeTransactionID int64  `json:"recharge_transaction_id"`
		RelatedTransactionID  int64  `json:"related_transaction_id"`
	} `json:"items"`
	NextCursor string `json:"next_cursor"`
}

func TestAdminReceiptReviewsFilterAllPendingPagesAndReviewTransitions(t *testing.T) {
	e := newEnv(t)
	employee := e.newEmployee("REVIEW-001", "13800000981", "复核员工")
	const total = 205
	for i := 0; i < total; i++ {
		e.recharge(employee.id, 100, fmt.Sprintf("REVIEW-R-%03d", i))
	}
	var originalID int64
	if err := e.db.QueryRowContext(t.Context(), `SELECT id FROM transactions WHERE type='RECHARGE' ORDER BY id DESC LIMIT 1`).Scan(&originalID); err != nil {
		t.Fatal(err)
	}
	e.post(e.public.URL, fmt.Sprintf("/api/admin/recharges/%d/reverse", originalID), e.adminToken, map[string]string{
		"idempotency_key": "receipt-review-reversal", "reason": "撤销重复充值",
	}).expect(t, http.StatusCreated, nil)

	// The old endpoint silently capped results at 200; collect every pending row via stable cursors.
	cursor := ""
	seen := map[int64]bool{}
	for {
		path := "/api/admin/receipt-reviews?limit=200"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var page receiptReviewPage
		e.get(e.public.URL, path, e.adminToken).expect(t, http.StatusOK, &page)
		for _, row := range page.Items {
			if row.Status != "PENDING" {
				t.Fatalf("default list status = %q, want PENDING", row.Status)
			}
			if seen[row.TransactionID] {
				t.Fatalf("transaction %d repeated across pages", row.TransactionID)
			}
			seen[row.TransactionID] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != total+1 {
		t.Fatalf("cursor pages returned %d rows, want %d", len(seen), total+1)
	}

	localDate := time.Now().In(e.location).Format("2006-01-02")
	var filtered receiptReviewPage
	e.get(e.public.URL, fmt.Sprintf("/api/admin/receipt-reviews?employee_id=%d&business_date=%s&status=PENDING&limit=200", employee.id, localDate), e.adminToken).
		expect(t, http.StatusOK, &filtered)
	if len(filtered.Items) != 200 || filtered.NextCursor == "" {
		t.Fatalf("employee/date/status filters returned unexpected first page: %d rows cursor=%q", len(filtered.Items), filtered.NextCursor)
	}
	var reversalTransactionID, reversalReceiptID, reversalRechargeID, reversalRelatedID int64
	var reversalReceiptRef string
	for i := range filtered.Items {
		item := &filtered.Items[i]
		if item.EmployeeID != employee.id || item.EmployeeNo != "REVIEW-001" || item.EmployeeName != "复核员工" || item.Department == "" || item.TransactionNo == "" {
			t.Fatalf("missing stable employee or transaction identity: %+v", item)
		}
		if item.Type == "RECHARGE_REVERSAL" {
			reversalTransactionID = item.TransactionID
			reversalReceiptID = item.ReceiptID
			reversalReceiptRef = item.ReceiptRef
			reversalRechargeID = item.RechargeTransactionID
			reversalRelatedID = item.RelatedTransactionID
		}
	}
	if reversalTransactionID == 0 || reversalReceiptID == 0 || reversalReceiptRef == "" || reversalRechargeID != originalID || reversalRelatedID != originalID {
		t.Fatalf("reversal row lost source receipt/entry identity: transaction=%d receipt=%d ref=%q recharge=%d related=%d", reversalTransactionID, reversalReceiptID, reversalReceiptRef, reversalRechargeID, reversalRelatedID)
	}
	var onePage receiptReviewPage
	e.get(e.public.URL, "/api/admin/receipt-reviews?business_date="+localDate+"&limit=1", e.adminToken).expect(t, http.StatusOK, &onePage)
	if len(onePage.Items) != 1 || onePage.NextCursor == "" {
		t.Fatalf("limit/cursor contract invalid: %+v", onePage)
	}
	for _, path := range []string{"?status=INVALID", "?employee_id=abc", "?business_date=2026-99-99", "?cursor=abc", "?business_date=2026-09-30&from=2026-09-30"} {
		if response := e.get(e.public.URL, "/api/admin/receipt-reviews"+path, e.adminToken); response.status != http.StatusBadRequest {
			t.Fatalf("invalid filter %s status=%d, want 400", path, response.status)
		}
	}

	before := e.balance(employee.id)
	// The recording administrator cannot review their own receipt.
	e.post(e.public.URL, fmt.Sprintf("/api/admin/receipt-reviews/%d", originalID), e.adminToken, map[string]string{"status": "MATCHED"}).expect(t, http.StatusConflict, nil)
	if got := e.balance(employee.id); got != before {
		t.Fatalf("self-review changed balance: before=%d after=%d", before, got)
	}
	if response := e.post(e.public.URL, "/api/admin/receipt-reviews", e.adminToken, map[string]any{"transaction_id": originalID, "status": "DIFFERENCE", "note": "  "}); response.status != http.StatusBadRequest {
		t.Fatalf("blank difference note status=%d, want 400", response.status)
	}

	if _, err := e.admins.CreateAdmin(t.Context(), "receipt-reviewer", "receipt-reviewer-password"); err != nil {
		t.Fatal(err)
	}
	reviewer := e.loginAdmin("receipt-reviewer", "receipt-reviewer-password", "")
	if response := e.post(e.public.URL, fmt.Sprintf("/api/admin/receipt-reviews/%d", originalID), reviewer, map[string]string{"status": "RESOLVED", "note": "已核对"}); response.status != http.StatusConflict {
		t.Fatalf("resolve before difference status=%d, want 409", response.status)
	}
	e.post(e.public.URL, fmt.Sprintf("/api/admin/receipt-reviews/%d", originalID), reviewer, map[string]string{"status": "DIFFERENCE", "note": "收款凭据金额需确认"}).expect(t, http.StatusOK, nil)
	if response := e.post(e.public.URL, "/api/admin/receipt-reviews", reviewer, map[string]any{"transaction_id": originalID, "status": "MATCHED"}); response.status != http.StatusConflict {
		t.Fatalf("difference-to-matched status=%d, want 409", response.status)
	}
	e.post(e.public.URL, fmt.Sprintf("/api/admin/receipt-reviews/%d", originalID), reviewer, map[string]string{"status": "RESOLVED", "note": "已补齐核对记录"}).expect(t, http.StatusOK, nil)
	var matchedID int64
	if err := e.db.QueryRowContext(t.Context(), `SELECT id FROM transactions WHERE type='RECHARGE' AND id<>? ORDER BY id DESC LIMIT 1`, originalID).Scan(&matchedID); err != nil {
		t.Fatal(err)
	}
	e.post(e.public.URL, "/api/admin/receipt-reviews", reviewer, map[string]any{"transaction_id": matchedID, "status": "MATCHED"}).expect(t, http.StatusOK, nil)
	if got := e.balance(employee.id); got != before {
		t.Fatalf("receipt reviews changed balance: before=%d after=%d", before, got)
	}

	var resolved receiptReviewPage
	e.get(e.public.URL, "/api/admin/receipt-reviews?status=RESOLVED&employee_id="+fmt.Sprint(employee.id), reviewer).expect(t, http.StatusOK, &resolved)
	if len(resolved.Items) != 1 || resolved.Items[0].TransactionID != originalID || resolved.Items[0].Status != "RESOLVED" || resolved.Items[0].ReviewerID == 0 || resolved.Items[0].Note != "已补齐核对记录" {
		t.Fatalf("resolved status filter or reviewer fields incorrect: %+v", resolved)
	}
	var matched receiptReviewPage
	e.get(e.public.URL, "/api/admin/receipt-reviews?status=MATCHED&employee_id="+fmt.Sprint(employee.id), reviewer).expect(t, http.StatusOK, &matched)
	if len(matched.Items) != 1 || matched.Items[0].TransactionID != matchedID || matched.Items[0].Status != "MATCHED" {
		t.Fatalf("matched status filter incorrect: %+v", matched)
	}
	if unauthenticated := e.get(e.public.URL, "/api/admin/receipt-reviews", ""); unauthenticated.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list status=%d, want 401", unauthenticated.status)
	}
}
