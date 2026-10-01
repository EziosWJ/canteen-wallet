package acceptance

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func exportCSV(t *testing.T, e *env, path string, wantStatus int) [][]string {
	t.Helper()
	response := e.get(e.public.URL, path, e.adminToken).expect(t, wantStatus, nil)
	if wantStatus != http.StatusOK {
		return nil
	}
	if !strings.HasPrefix(response.header("Content-Type"), "text/csv") {
		t.Fatalf("export content type = %q", response.header("Content-Type"))
	}
	if !strings.HasPrefix(string(response.body), "\xef\xbb\xbf") {
		t.Fatal("CSV is missing UTF-8 BOM")
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(response.body), "\xef\xbb\xbf"))).ReadAll()
	if err != nil {
		t.Fatalf("parse exported CSV: %v", err)
	}
	return rows
}

func TestAdminExportsUseListFiltersAndIncludeAllMatches(t *testing.T) {
	e := newEnv(t)
	formula := e.newEmployee("EXP-001", "13900000101", "=1+2")
	e.adjustBalance(formula.id, 275, "export")
	for i := 0; i < 105; i++ {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		number := fmt.Sprintf("BULK-%03d", i)
		phone := fmt.Sprintf("13900000%03d", i+200)
		result, err := e.db.ExecContext(t.Context(), `INSERT INTO employees(employee_no,name,phone,department,status,password_hash,must_change_password,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, number, "批量员工", phone, "后勤", "ACTIVE", "acceptance-placeholder", 1, now, now)
		if err != nil {
			t.Fatalf("insert export employee %s: %v", number, err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("read inserted export employee ID: %v", err)
		}
		if _, err := e.db.ExecContext(t.Context(), `INSERT INTO accounts(employee_id,balance,status,created_at,updated_at) VALUES(?,?,?,?,?)`, id, int64(0), "ACTIVE", now, now); err != nil {
			t.Fatalf("insert export account for %s: %v", number, err)
		}
	}

	rows := exportCSV(t, e, "/api/admin/exports/employees", http.StatusOK)
	if len(rows) != 107 { // header + the API-created employee + 105 fixture employees
		t.Fatalf("employees export returned %d CSV lines, want 107", len(rows))
	}
	if got := e.get(e.public.URL, "/api/admin/exports/employees", e.adminToken).header("Content-Disposition"); !strings.Contains(got, "employees-all.csv") {
		t.Fatalf("all export filename does not identify full employee export: %q", got)
	}
	filtered := exportCSV(t, e, "/api/admin/exports/employees?q="+url.QueryEscape("EXP-001")+"&department=%E7%A0%94%E5%8F%91&account_status=ACTIVE", http.StatusOK)
	if len(filtered) != 2 || filtered[1][0] != "EXP-001" || filtered[1][1] != "'=1+2" {
		t.Fatalf("filtered employee export or formula escaping failed: %+v", filtered)
	}
	response := e.get(e.public.URL, "/api/admin/exports/employees?q=no-match&account_status=CLOSED&all=1", e.adminToken).
		expect(t, http.StatusOK, nil)
	if !strings.Contains(response.header("Content-Disposition"), "employees-all.csv") {
		t.Fatalf("all=1 did not select a full-export filename: %q", response.header("Content-Disposition"))
	}
	allRows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(response.body), "\xef\xbb\xbf"))).ReadAll()
	if err != nil || len(allRows) != 107 {
		t.Fatalf("all=1 did not ignore filters; lines=%d err=%v", len(allRows), err)
	}

	balances := exportCSV(t, e, "/api/admin/exports/balances?department=%E7%A0%94%E5%8F%91", http.StatusOK)
	if len(balances) != 2 || balances[1][2] != "275" {
		t.Fatalf("balance export did not preserve integer cents and department filter: %+v", balances)
	}
	if got := e.get(e.public.URL, "/api/admin/exports/balances?department=%E7%A0%94%E5%8F%91", e.adminToken).header("Content-Disposition"); !strings.Contains(got, "balances-department.csv") {
		t.Fatalf("filtered export filename does not identify type and filter: %q", got)
	}

	e.recharge(formula.id, 1200, "export-recharge")
	transactions := exportCSV(t, e, fmt.Sprintf("/api/admin/exports/transactions?employee_id=%d&type=RECHARGE", formula.id), http.StatusOK)
	if len(transactions) != 2 || transactions[1][1] != "RECHARGE" || transactions[1][2] != "1200" {
		t.Fatalf("transaction export filter or integer-cents amount failed: %+v", transactions)
	}
	if empty := e.get(e.public.URL, "/api/admin/exports/transactions?type=REFUND", e.adminToken).expect(t, http.StatusNoContent, nil); empty.header("X-Export-Empty") != "true" || empty.header("X-Export-Row-Count") != "0" {
		t.Fatalf("empty export response is unclear: headers=%v", empty.headers)
	}
	if invalid := e.get(e.public.URL, "/api/admin/exports/transactions?type=NOT_A_TYPE", e.adminToken); invalid.status != http.StatusBadRequest {
		t.Fatalf("invalid transaction filter status = %d, want 400", invalid.status)
	}
	if invalid := e.get(e.public.URL, "/api/admin/exports/employees?unknown=x", e.adminToken); invalid.status != http.StatusBadRequest {
		t.Fatalf("unknown filter status = %d, want 400", invalid.status)
	}
}

func TestAdminReconciliationExportFiltersDateAndRequiresAdmin(t *testing.T) {
	e := newEnv(t)
	date := time.Now().In(e.location).Format("2006-01-02")
	e.post(e.public.URL, "/api/admin/reconciliation/daily", e.adminToken, map[string]string{"business_date": date}).expect(t, http.StatusCreated, nil)
	rows := exportCSV(t, e, "/api/admin/exports/reconciliation?business_date="+date, http.StatusOK)
	if len(rows) != 2 || rows[1][0] != date {
		t.Fatalf("daily reconciliation export did not match selected date: %+v", rows)
	}
	if got := e.get(e.public.URL, "/api/admin/exports/reconciliation?business_date="+date, e.adminToken).header("Content-Disposition"); !strings.Contains(got, "reconciliation-business_date.csv") {
		t.Fatalf("reconciliation filename does not identify selected filter: %q", got)
	}
	if unauthenticated := e.get(e.public.URL, "/api/admin/exports/reconciliation", ""); unauthenticated.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated export status = %d, want 401", unauthenticated.status)
	}
	if invalid := e.get(e.public.URL, "/api/admin/exports/reconciliation?business_date=2026-99-99", e.adminToken); invalid.status != http.StatusBadRequest {
		t.Fatalf("invalid date filter status = %d, want 400", invalid.status)
	}
}
