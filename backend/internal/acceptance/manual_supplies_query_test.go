package acceptance

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"
)

type manualSupplyList struct {
	Items []struct {
		ID                   int64  `json:"id"`
		ReceiptRef           string `json:"receipt_ref"`
		EmployeeID           int64  `json:"employee_id"`
		EmployeeName         string `json:"employee_name"`
		EmployeeNo           string `json:"employee_no"`
		Department           string `json:"department"`
		EmployeeStatus       string `json:"employee_status"`
		AccountStatus        string `json:"account_status"`
		MealCode             string `json:"meal_code"`
		MealName             string `json:"meal_name"`
		BusinessDate         string `json:"business_date"`
		AmountCents          int64  `json:"amount_cents"`
		Status               string `json:"status"`
		Note                 string `json:"note"`
		ExceptionReason      string `json:"exception_reason"`
		TransactionID        int64  `json:"transaction_id"`
		CreatedAt            string `json:"created_at"`
		ResolvedAt           string `json:"resolved_at"`
		TransactionCreatedAt string `json:"transaction_created_at"`
	} `json:"items"`
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

func registerManualSupply(e *env, receipt string, employeeID int64, date string, amount int64) int64 {
	e.t.Helper()
	var result struct {
		ID       int64  `json:"id"`
		Status   string `json:"status"`
		Replayed bool   `json:"replayed"`
	}
	e.post(e.public.URL, "/api/admin/manual-supplies", e.adminToken, map[string]any{
		"receipt_ref": receipt, "employee_id": employeeID, "meal_code": "LUNCH",
		"business_date": date, "amount_cents": amount, "note": "设备故障登记",
	}).expect(e.t, http.StatusCreated, &result)
	if result.ID < 1 || result.Status != "PENDING" || result.Replayed {
		e.t.Fatalf("unexpected registration result: %+v", result)
	}
	return result.ID
}

func manualSupplyURL(values url.Values) string {
	if len(values) == 0 {
		return "/api/admin/manual-supplies"
	}
	return "/api/admin/manual-supplies?" + values.Encode()
}

func TestManualSupplyListFiltersBeyondFirstTwoHundredAndPagesByID(t *testing.T) {
	e := newEnv(t)
	worker := e.newEmployee("MS-QUERY-1", "13800001021", "林晓")
	firstID := registerManualSupply(e, "MS-QUERY-POSTED", worker.id, "2026-09-29", 500)
	e.recharge(worker.id, 2000, "MS-QUERY-RECHARGE")
	e.post(e.public.URL, fmt.Sprintf("/api/admin/manual-supplies/%d/post", firstID), e.adminToken,
		map[string]string{"idempotency_key": "ms-query-posted"}).expect(t, http.StatusCreated, nil)
	for i := 0; i < 202; i++ {
		registerManualSupply(e, fmt.Sprintf("MS-QUERY-%03d", i), worker.id, "2026-09-30", 100)
	}

	var pending manualSupplyList
	e.get(e.public.URL, manualSupplyURL(nil), e.adminToken).expect(t, http.StatusOK, &pending)
	if len(pending.Items) != 50 || !pending.HasMore || pending.NextCursor == "" {
		t.Fatalf("default pending page should contain 50 rows and a cursor: got %d, has_more=%t, cursor=%q", len(pending.Items), pending.HasMore, pending.NextCursor)
	}
	if pending.Items[0].ID <= pending.Items[len(pending.Items)-1].ID {
		t.Fatalf("default page is not in descending ID order: %d <= %d", pending.Items[0].ID, pending.Items[len(pending.Items)-1].ID)
	}
	seen := make(map[int64]bool)
	page := pending
	pageCount := 1
	for page.HasMore {
		values := url.Values{"cursor": {page.NextCursor}, "limit": {"50"}}
		var next manualSupplyList
		e.get(e.public.URL, manualSupplyURL(values), e.adminToken).expect(t, http.StatusOK, &next)
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatalf("ID %d repeated across cursor pages", item.ID)
			}
			seen[item.ID] = true
		}
		page = next
		pageCount++
	}
	for _, item := range page.Items {
		if seen[item.ID] {
			t.Fatalf("ID %d repeated across cursor pages", item.ID)
		}
		seen[item.ID] = true
	}
	if len(seen) != 202 || pageCount != 5 {
		t.Fatalf("cursor pages returned %d pending registrations over %d pages, want 202 over 5", len(seen), pageCount)
	}

	var posted manualSupplyList
	e.get(e.public.URL, manualSupplyURL(url.Values{"status": {"POSTED"}}), e.adminToken).expect(t, http.StatusOK, &posted)
	if len(posted.Items) != 1 || posted.Items[0].ID != firstID {
		t.Fatalf("status filter did not find the older posted row: %+v", posted.Items)
	}
	item := posted.Items[0]
	if item.EmployeeName != "林晓" || item.EmployeeNo != "MS-QUERY-1" || item.Department != "研发" || item.EmployeeStatus != "ACTIVE" || item.AccountStatus != "ACTIVE" {
		t.Fatalf("identity snapshot fields missing or wrong: %+v", item)
	}
	if item.MealCode != "LUNCH" || item.MealName != "午餐" || item.BusinessDate != "2026-09-29" {
		t.Fatalf("meal/date fields missing or wrong: %+v", item)
	}
	if item.TransactionID == 0 || item.CreatedAt == "" || item.ResolvedAt == "" || item.TransactionCreatedAt == "" {
		t.Fatalf("posted row should include creation, resolution, and transaction timestamps: %+v", item)
	}
	var all manualSupplyList
	e.get(e.public.URL, manualSupplyURL(url.Values{"status": {"all"}, "limit": {"200"}}), e.adminToken).expect(t, http.StatusOK, &all)
	if len(all.Items) != 200 || !all.HasMore {
		t.Fatalf("all status filter should paginate the complete set: got %d, has_more=%t", len(all.Items), all.HasMore)
	}
}

func TestManualSupplyRegistrationIsPendingUntilPostAndRetryDoesNotDoubleDebit(t *testing.T) {
	e := newEnv(t)
	worker := e.newEmployee("MS-POST-1", "13800001022", "周宁")
	date := time.Now().In(e.location).Format("2006-01-02")
	id := registerManualSupply(e, "MS-POST-RETRY", worker.id, date, 1500)
	if got := e.balance(worker.id); got != 0 {
		t.Fatalf("registration must not debit the account, balance=%d", got)
	}
	if got := len(e.transactions(worker.id, "CONSUME")); got != 0 {
		t.Fatalf("registration must not create a consumption transaction, count=%d", got)
	}
	var list manualSupplyList
	e.get(e.public.URL, manualSupplyURL(url.Values{"status": {"PENDING"}}), e.adminToken).expect(t, http.StatusOK, &list)
	if len(list.Items) != 1 || list.Items[0].ID != id || list.Items[0].Status != "PENDING" {
		t.Fatalf("registration should appear as pending: %+v", list.Items)
	}
	var failed struct {
		Status string `json:"status"`
		Code   string `json:"code"`
	}
	e.post(e.public.URL, fmt.Sprintf("/api/admin/manual-supplies/%d/post", id), e.adminToken,
		map[string]string{"idempotency_key": "ms-post-first-attempt"}).expect(t, http.StatusConflict, &failed)
	if failed.Status != "EXCEPTION" || failed.Code != "INSUFFICIENT_FUNDS" {
		t.Fatalf("low balance should produce the insufficient funds exception: %+v", failed)
	}
	var exception manualSupplyList
	e.get(e.public.URL, manualSupplyURL(url.Values{"status": {"EXCEPTION"}}), e.adminToken).expect(t, http.StatusOK, &exception)
	if len(exception.Items) != 1 || exception.Items[0].ExceptionReason != "INSUFFICIENT_FUNDS" {
		t.Fatalf("exception list should report the reason: %+v", exception.Items)
	}
	e.recharge(worker.id, 3000, "MS-POST-TOPUP")
	e.post(e.public.URL, fmt.Sprintf("/api/admin/manual-supplies/%d/post", id), e.adminToken,
		map[string]string{"idempotency_key": "ms-post-retry-after-topup"}).expect(t, http.StatusCreated, nil)
	if got := e.balance(worker.id); got != 1500 {
		t.Fatalf("successful retry should debit once, balance=%d want 1500", got)
	}
	var replay struct {
		Status        string `json:"status"`
		TransactionID int64  `json:"transaction_id"`
		Replayed      bool   `json:"replayed"`
	}
	e.post(e.public.URL, fmt.Sprintf("/api/admin/manual-supplies/%d/post", id), e.adminToken,
		map[string]string{"idempotency_key": "ms-post-third-attempt"}).expect(t, http.StatusOK, &replay)
	if replay.Status != "POSTED" || replay.TransactionID == 0 || !replay.Replayed {
		t.Fatalf("repeat posting must replay original transaction: %+v", replay)
	}
	if got := e.balance(worker.id); got != 1500 {
		t.Fatalf("repeat posting debited more than once, balance=%d", got)
	}
	if got := len(e.transactions(worker.id, "CONSUME")); got != 1 {
		t.Fatalf("repeat posting created %d consumption transactions, want 1", got)
	}
}
