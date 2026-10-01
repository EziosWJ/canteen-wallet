package httpapi

import (
	"crypto/rand"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/employees"
	"github.com/EziosWJ/canteen-wallet/backend/internal/xlsx"
)

type importRow struct {
	Row        int    `json:"row"`
	EmployeeNo string `json:"employee_no"`
	Name       string `json:"name"`
	Phone      string `json:"phone"`
	Department string `json:"department"`
	Status     string `json:"status"`
}
type importError struct {
	Row     int    `json:"row"`
	Message string `json:"message"`
}

var employeeNoFormat = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
var phoneFormat = regexp.MustCompile(`^1[3-9][0-9]{9}$`)

func adminImportExportRoutes(admin *http.ServeMux, db *sql.DB, employeeService *employees.Service) {
	admin.HandleFunc("/api/admin/imports/template", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodGet(w)
			return
		}
		body, err := xlsx.EmployeeImportTemplate()
		if err != nil {
			unavailable(w)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition", `attachment; filename="employee-import-template.xlsx"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	admin.HandleFunc("/api/admin/imports/preview", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodPost(w)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 6<<20)
		if err := r.ParseMultipartForm(5 << 20); err != nil {
			WriteError(w, 400, CodeInvalidRequest, "invalid upload")
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			WriteError(w, 400, CodeInvalidRequest, "XLSX file required")
			return
		}
		defer file.Close()
		body, err := io.ReadAll(io.LimitReader(file, (5<<20)+1))
		if err != nil || len(body) > 5<<20 {
			WriteError(w, 400, CodeInvalidRequest, "file too large")
			return
		}
		matrix, err := xlsx.ReadFirstSheet(body)
		if err != nil {
			WriteError(w, 400, CodeInvalidRequest, "invalid XLSX workbook")
			return
		}
		if len(matrix) < 2 || len(matrix[0]) < 5 || strings.Join(matrix[0][:5], "|") != "employee_no|name|phone|department|status" {
			WriteError(w, 400, CodeInvalidRequest, "header must be employee_no,name,phone,department,status")
			return
		}
		rows := make([]importRow, 0)
		problems := make([]importError, 0)
		seenNo := map[string]bool{}
		seenPhone := map[string]bool{}
		for index, values := range matrix[1:] {
			item := importRow{Row: index + 2}
			if len(values) > 0 {
				item.EmployeeNo = values[0]
			}
			if len(values) > 1 {
				item.Name = values[1]
			}
			if len(values) > 2 {
				item.Phone = values[2]
			}
			if len(values) > 3 {
				item.Department = values[3]
			}
			if len(values) > 4 {
				item.Status = values[4]
			}
			if item.EmployeeNo == "" && item.Name == "" && item.Phone == "" && item.Department == "" {
				continue
			}
			if item.Status == "" {
				item.Status = "ACTIVE"
			}
			rows = append(rows, item)
			if len(values) < 5 || !employeeNoFormat.MatchString(item.EmployeeNo) || len(item.Name) < 1 || len(item.Name) > 100 || !phoneFormat.MatchString(item.Phone) || len(item.Department) < 1 || len(item.Department) > 100 || (item.Status != "ACTIVE" && item.Status != "FROZEN") {
				problems = append(problems, importError{item.Row, "invalid employee fields"})
				continue
			}
			if seenNo[strings.ToLower(item.EmployeeNo)] || seenPhone[item.Phone] {
				problems = append(problems, importError{item.Row, "duplicate in workbook"})
				continue
			}
			seenNo[strings.ToLower(item.EmployeeNo)] = true
			seenPhone[item.Phone] = true
			var count int
			if err := db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM employees WHERE employee_no=? OR phone=?`, item.EmployeeNo, item.Phone).Scan(&count); err != nil {
				unavailable(w)
				return
			}
			if count > 0 {
				problems = append(problems, importError{item.Row, "employee number or phone already exists"})
			}
		}
		if len(rows) == 0 {
			WriteError(w, 400, CodeInvalidRequest, "workbook has no employees")
			return
		}
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			unavailable(w)
			return
		}
		id := hex.EncodeToString(raw[:])
		rowsJSON, _ := json.Marshal(rows)
		errorsJSON, _ := json.Marshal(problems)
		_, err = db.ExecContext(r.Context(), `INSERT INTO import_previews(id,administrator_id,rows_json,errors_json,created_at) VALUES(?,?,?,?,?)`, id, principalFromContext(r.Context()).ID, string(rowsJSON), string(errorsJSON), time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			unavailable(w)
			return
		}
		writeJSON(w, 200, map[string]any{"preview_id": id, "rows": rows, "errors": problems,
			"total_rows": len(rows), "valid_rows": len(rows) - len(problems), "error_rows": len(problems)})
	})
	admin.HandleFunc("/api/admin/imports/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/admin/imports/")
		parts := strings.Split(path, "/")
		if len(parts) != 2 || (parts[1] != "confirm" && parts[1] != "errors") {
			notFound(w, r)
			return
		}
		if parts[1] == "errors" {
			if r.Method != http.MethodGet {
				methodGet(w)
				return
			}
			var encoded string
			err := db.QueryRowContext(r.Context(), `SELECT errors_json FROM import_previews WHERE id=? AND administrator_id=?`, parts[0], principalFromContext(r.Context()).ID).Scan(&encoded)
			if errors.Is(err, sql.ErrNoRows) {
				WriteError(w, 404, CodeNotFound, "preview not found")
				return
			}
			if err != nil {
				unavailable(w)
				return
			}
			var problems []importError
			if json.Unmarshal([]byte(encoded), &problems) != nil {
				unavailable(w)
				return
			}
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="import-errors.csv"`)
			writer := csv.NewWriter(w)
			_ = writer.Write([]string{"row", "message"})
			for _, problem := range problems {
				_ = writer.Write([]string{strconv.Itoa(problem.Row), csvSafe(problem.Message)})
			}
			writer.Flush()
			return
		}
		if r.Method != http.MethodPost {
			methodPost(w)
			return
		}
		id := parts[0]
		if len(id) != 32 {
			WriteError(w, 400, CodeInvalidRequest, "invalid preview")
			return
		}
		var rowsJSON, errorsJSON string
		var confirmed sql.NullString
		var createdAt string
		err := db.QueryRowContext(r.Context(), `SELECT rows_json,errors_json,confirmed_at,created_at FROM import_previews WHERE id=? AND administrator_id=?`, id, principalFromContext(r.Context()).ID).Scan(&rowsJSON, &errorsJSON, &confirmed, &createdAt)
		if errors.Is(err, sql.ErrNoRows) {
			WriteError(w, 404, CodeNotFound, "preview not found")
			return
		}
		if err != nil {
			unavailable(w)
			return
		}
		createdTime, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			unavailable(w)
			return
		}
		if confirmed.Valid {
			WriteError(w, 409, CodeConflict, "preview already confirmed")
			return
		}
		if time.Since(createdTime) > time.Hour {
			WriteError(w, http.StatusGone, CodeConflict, "preview expired; upload the file again")
			return
		}
		var rows []importRow
		var problems []importError
		if json.Unmarshal([]byte(rowsJSON), &rows) != nil || json.Unmarshal([]byte(errorsJSON), &problems) != nil {
			unavailable(w)
			return
		}
		invalidRows := map[int]bool{}
		for _, problem := range problems {
			invalidRows[problem.Row] = true
		}
		created := make([]map[string]any, 0)
		// Claim the preview before creating records. A replay (including a
		// concurrent second request) can never receive temporary passwords again.
		claimedAt := time.Now().UTC().Format(time.RFC3339Nano)
		claim, err := db.ExecContext(r.Context(), `UPDATE import_previews SET confirmed_at=? WHERE id=? AND administrator_id=? AND confirmed_at IS NULL`, claimedAt, id, principalFromContext(r.Context()).ID)
		if err != nil {
			unavailable(w)
			return
		}
		if n, _ := claim.RowsAffected(); n != 1 {
			WriteError(w, 409, CodeConflict, "preview already confirmed")
			return
		}
		for _, item := range rows {
			if invalidRows[item.Row] {
				continue
			}
			employee, password, err := employeeService.Create(r.Context(), principalFromContext(r.Context()).ID, employees.CreateInput{EmployeeNo: item.EmployeeNo, Name: item.Name, Phone: item.Phone, Department: item.Department, Status: item.Status})
			if err != nil {
				message := "could not create employee"
				if errors.Is(err, employees.ErrConflict) {
					message = "employee number or phone already exists"
				}
				problems = append(problems, importError{item.Row, message})
				continue
			}
			created = append(created, map[string]any{"employee": employee, "temporary_password": password})
		}
		encodedProblems, _ := json.Marshal(problems)
		_, err = db.ExecContext(r.Context(), `UPDATE import_previews SET errors_json=? WHERE id=? AND administrator_id=?`, string(encodedProblems), id, principalFromContext(r.Context()).ID)
		if err != nil {
			unavailable(w)
			return
		}
		writeJSON(w, 200, map[string]any{"created": created, "errors": problems,
			"total_rows": len(rows), "created_rows": len(created), "error_rows": len(problems)})
	})
	admin.HandleFunc("/api/admin/exports/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodGet(w)
			return
		}
		kind := strings.TrimPrefix(r.URL.Path, "/api/admin/exports/")
		var query string
		var header []string
		var args []any
		allowed := map[string]bool{"all": true}
		switch kind {
		case "employees":
			allowed["q"], allowed["department"], allowed["account_status"] = true, true, true
			where := ` WHERE (?='' OR e.name LIKE '%' || ? || '%' ESCAPE '\' COLLATE NOCASE OR e.employee_no LIKE '%' || ? || '%' ESCAPE '\' COLLATE NOCASE OR e.phone LIKE '%' || ? || '%' ESCAPE '\' COLLATE NOCASE)
				AND (?='' OR e.department=? COLLATE NOCASE) AND (?='' OR a.status=?)`
			query = `SELECT e.employee_no,e.name,e.phone,e.department,e.status FROM employees e JOIN accounts a ON a.employee_id=e.id` + where + ` ORDER BY e.id`
			header = []string{"employee_no", "name", "phone", "department", "status"}
			args = append(args, "", "", "", "", "", "", "", "")
		case "balances":
			allowed["q"], allowed["department"], allowed["account_status"] = true, true, true
			where := ` WHERE (?='' OR e.name LIKE '%' || ? || '%' ESCAPE '\' COLLATE NOCASE OR e.employee_no LIKE '%' || ? || '%' ESCAPE '\' COLLATE NOCASE OR e.phone LIKE '%' || ? || '%' ESCAPE '\' COLLATE NOCASE)
				AND (?='' OR e.department=? COLLATE NOCASE) AND (?='' OR a.status=?)`
			query = `SELECT e.employee_no,e.name,a.balance,a.status FROM accounts a JOIN employees e ON e.id=a.employee_id` + where + ` ORDER BY e.id`
			header = []string{"employee_no", "name", "balance_cents", "status"}
			args = append(args, "", "", "", "", "", "", "", "")
		case "transactions":
			allowed["employee_id"], allowed["type"], allowed["from"], allowed["to"] = true, true, true, true
			query = `SELECT t.transaction_no,t.type,t.amount,t.before_balance,t.after_balance,t.business_type,t.business_id,t.created_at FROM transactions t JOIN accounts a ON a.id=t.account_id JOIN employees e ON e.id=a.employee_id
				WHERE (?=0 OR e.id=?) AND (?='' OR t.type=?) AND (?='' OR t.created_at>=?) AND (?='' OR t.created_at<?) ORDER BY t.id`
			header = []string{"transaction_no", "type", "amount_cents", "before_balance_cents", "after_balance_cents", "business_type", "business_id", "created_at"}
			args = append(args, int64(0), int64(0), "", "", "", "", "", "")
		case "reconciliation":
			allowed["business_date"], allowed["from"], allowed["to"] = true, true, true
			query = `SELECT business_date,opening_cents,movement_cents,expected_cents,actual_cents,difference_cents FROM daily_reconciliation WHERE (?='' OR business_date=?) AND (?='' OR business_date>=?) AND (?='' OR business_date<=?) ORDER BY business_date`
			header = []string{"business_date", "opening_cents", "movement_cents", "expected_cents", "actual_cents", "difference_cents"}
			args = append(args, "", "", "", "", "", "")
		default:
			notFound(w, r)
			return
		}
		values := r.URL.Query()
		all := values.Get("all") == "1"
		if len(values["all"]) > 1 || (len(values["all"]) == 1 && values.Get("all") != "1") {
			WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "all must be 1")
			return
		}
		if !all {
			for key, items := range values {
				if !allowed[key] || len(items) != 1 {
					WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid export filter")
					return
				}
			}
			get := func(key string) string { return values.Get(key) }
			switch kind {
			case "employees", "balances":
				status := strings.ToUpper(strings.TrimSpace(get("account_status")))
				if status != "" && status != "ACTIVE" && status != "FROZEN" && status != "CLOSED" {
					WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid account_status")
					return
				}
				q, department := strings.TrimSpace(get("q")), strings.TrimSpace(get("department"))
				q = strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(q)
				args = []any{q, q, q, q, department, department, status, status}
			case "transactions":
				id := int64(0)
				if raw := get("employee_id"); raw != "" {
					parsed, err := strconv.ParseInt(raw, 10, 64)
					if err != nil || parsed < 1 {
						WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid employee_id")
						return
					}
					id = parsed
				}
				typeFilter := get("type")
				if typeFilter != "" && typeFilter != "RECHARGE" && typeFilter != "RECHARGE_REVERSAL" && typeFilter != "CONSUME" && typeFilter != "REFUND" && typeFilter != "BALANCE_ADJUSTMENT" && typeFilter != "BALANCE_WITHDRAWAL" {
					WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid transaction type")
					return
				}
				from, to := get("from"), get("to")
				if from != "" {
					if _, err := time.Parse("2006-01-02", from); err != nil {
						WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid from date")
						return
					}
					from += "T00:00:00"
				}
				if to != "" {
					day, err := time.Parse("2006-01-02", to)
					if err != nil {
						WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid to date")
						return
					}
					to = day.AddDate(0, 0, 1).Format("2006-01-02") + "T00:00:00"
				}
				if from != "" && to != "" && from >= to {
					WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid transaction date range")
					return
				}
				args = []any{id, id, typeFilter, typeFilter, from, from, to, to}
			case "reconciliation":
				date, from, to := get("business_date"), get("from"), get("to")
				for _, candidate := range []struct{ name, value string }{{"business_date", date}, {"from", from}, {"to", to}} {
					if candidate.value != "" {
						if _, err := time.Parse("2006-01-02", candidate.value); err != nil {
							WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid "+candidate.name+" date")
							return
						}
					}
				}
				if date != "" && (from != "" || to != "") || from != "" && to != "" && from > to {
					WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid reconciliation date range")
					return
				}
				args = []any{date, date, from, from, to, to}
			}
		}
		// The API handlers use the same filters as their corresponding list pages.
		filtered := false
		for key, items := range values {
			if key != "all" && len(items) == 1 && items[0] != "" {
				filtered = true
				break
			}
		}
		filename := kind + "-all.csv"
		if !all && filtered {
			used := make([]string, 0, len(values))
			for key := range values {
				if key != "all" && values.Get(key) != "" {
					used = append(used, key)
				}
			}
			// Stable parameter order makes downloaded names predictable.
			order := []string{"q", "department", "account_status", "employee_id", "type", "from", "to", "business_date"}
			ordered := used[:0]
			for _, key := range order {
				for _, candidate := range used {
					if candidate == key {
						ordered = append(ordered, key)
						break
					}
				}
			}
			if len(ordered) == 0 {
				filename = kind + "-filtered.csv"
			} else {
				filename = kind + "-" + strings.Join(ordered, "-") + ".csv"
			}
		}
		rows, err := db.QueryContext(r.Context(), query, args...)
		if err != nil {
			unavailable(w)
			return
		}
		defer rows.Close()
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				unavailable(w)
				return
			}
			w.Header().Set("X-Export-Empty", "true")
			w.Header().Set("X-Export-Row-Count", "0")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
		w.Header().Set("X-Export-Empty", "false")
		w.Write([]byte{0xef, 0xbb, 0xbf})
		writer := csv.NewWriter(w)
		_ = writer.Write(header)
		writeCurrent := func() bool {
			values := make([]any, len(header))
			dest := make([]any, len(header))
			for index := range values {
				dest[index] = &values[index]
			}
			if err := rows.Scan(dest...); err != nil {
				return false
			}
			line := make([]string, len(header))
			for index, value := range values {
				switch item := value.(type) {
				case []byte:
					line[index] = csvSafe(string(item))
				case string:
					line[index] = csvSafe(item)
				case int64:
					line[index] = strconv.FormatInt(item, 10)
				case nil:
					line[index] = ""
				default:
					line[index] = csvSafe(fmt.Sprint(item))
				}
			}
			if err := writer.Write(line); err != nil {
				return false
			}
			return true
		}
		if !writeCurrent() {
			unavailable(w)
			return
		}
		for rows.Next() {
			if !writeCurrent() {
				unavailable(w)
				return
			}
		}
		if rows.Err() != nil {
			unavailable(w)
			return
		}
		writer.Flush()
	})
}

func csvSafe(value string) string {
	if value == "" {
		return value
	}
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed == "" {
		return value
	}
	first := trimmed[0]
	if first == '=' || first == '+' || first == '-' || first == '@' || value[0] == '\t' || value[0] == '\r' || value[0] == '\n' {
		return "'" + value
	}
	return value
}
