package acceptance

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

type employeeSearchResult struct {
	Employees []struct {
		ID            int64  `json:"id"`
		AccountID     int64  `json:"account_id"`
		EmployeeNo    string `json:"employee_no"`
		Name          string `json:"name"`
		Phone         string `json:"phone"`
		Department    string `json:"department"`
		Status        string `json:"status"`
		AccountStatus string `json:"account_status"`
		Balance       int64  `json:"balance"`
	} `json:"employees"`
	NextCursor string `json:"next_cursor"`
}

func TestAdminEmployeeSearchFiltersIdentityAndPaginates(t *testing.T) {
	e := newEnv(t)
	create := func(number, name, phone, department string) int64 {
		t.Helper()
		var result struct {
			Employee struct {
				ID        int64 `json:"id"`
				AccountID int64 `json:"account_id"`
			} `json:"employee"`
		}
		e.post(e.public.URL, "/api/admin/employees", e.adminToken, map[string]string{
			"employee_no": number, "name": name, "phone": phone, "department": department,
		}).expect(t, http.StatusCreated, &result)
		if result.Employee.ID == 0 || result.Employee.AccountID == 0 {
			t.Fatalf("created employee has no stable employee/account identity: %+v", result.Employee)
		}
		return result.Employee.ID
	}
	firstID := create("SEARCH-001", "同名员工", "13900000001", "研发")
	secondID := create("SEARCH-002", "同名员工", "13900000002", "财务")
	targetID := create("SEARCH-003", "跨页目标", "13900000003", "财务")
	e.request(http.MethodPatch, e.public.URL, fmt.Sprintf("/api/admin/employees/%d/status", targetID), e.adminToken, map[string]string{"status": "FROZEN"}).
		expect(t, http.StatusOK, nil)

	// The target is beyond the first two results and remains reachable through a
	// stable cursor. Every row carries identity and account fields for selection.
	var first employeeSearchResult
	e.get(e.public.URL, "/api/admin/employees?limit=2", e.adminToken).expect(t, http.StatusOK, &first)
	if len(first.Employees) != 2 || first.NextCursor == "" || first.Employees[0].ID != firstID || first.Employees[1].ID != secondID {
		t.Fatalf("unexpected first employee page: %+v", first)
	}
	var second employeeSearchResult
	e.get(e.public.URL, "/api/admin/employees?limit=2&cursor="+url.QueryEscape(first.NextCursor), e.adminToken).
		expect(t, http.StatusOK, &second)
	if len(second.Employees) != 1 || second.NextCursor != "" || second.Employees[0].ID != targetID {
		t.Fatalf("cursor did not reach employee after first page: %+v", second)
	}
	selected := second.Employees[0]
	if selected.AccountID == 0 || selected.EmployeeNo != "SEARCH-003" || selected.Name != "跨页目标" ||
		selected.Department != "财务" || selected.Balance != 0 || selected.AccountStatus != "FROZEN" {
		t.Fatalf("search result omitted or misreported identity/account fields: %+v", selected)
	}

	// Identity search spans employee number, name and phone; the exact department
	// and account-state filters compose with it to distinguish duplicate names.
	for _, q := range []string{"SEARCH-003", "跨页", "00000003"} {
		var result employeeSearchResult
		path := "/api/admin/employees?q=" + url.QueryEscape(q) + "&department=" + url.QueryEscape("财务") + "&account_status=FROZEN"
		e.get(e.public.URL, path, e.adminToken).expect(t, http.StatusOK, &result)
		if len(result.Employees) != 1 || result.Employees[0].ID != targetID {
			t.Fatalf("combined identity/department/status search for %q returned %+v", q, result.Employees)
		}
	}
	var sameName employeeSearchResult
	e.get(e.public.URL, "/api/admin/employees?q="+url.QueryEscape("同名员工"), e.adminToken).
		expect(t, http.StatusOK, &sameName)
	if len(sameName.Employees) != 2 || sameName.Employees[0].EmployeeNo == sameName.Employees[1].EmployeeNo {
		t.Fatalf("same-name employees cannot be distinguished by employee number: %+v", sameName.Employees)
	}

	// The no-parameter endpoint keeps the legacy response shape for existing
	// clients, while the paginated endpoint still requires admin authentication.
	var legacy struct {
		Employees []map[string]any `json:"employees"`
	}
	e.get(e.public.URL, "/api/admin/employees", e.adminToken).expect(t, http.StatusOK, &legacy)
	if len(legacy.Employees) != 3 {
		t.Fatalf("legacy employee GET returned %d records, want 3", len(legacy.Employees))
	}
	e.get(e.public.URL, "/api/admin/employees?limit=2", "").expect(t, http.StatusUnauthorized, nil)
	e.get(e.public.URL, "/api/admin/employees?account_status=UNKNOWN", e.adminToken).expect(t, http.StatusBadRequest, nil)
}
