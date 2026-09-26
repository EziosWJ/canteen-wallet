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
		if len(matrix) < 2 || strings.Join(matrix[0][:5], "|") != "employee_no|name|phone|department|status" {
			WriteError(w, 400, CodeInvalidRequest, "header must be employee_no,name,phone,department,status")
			return
		}
		rows := make([]importRow, 0)
		problems := make([]importError, 0)
		seenNo := map[string]bool{}
		seenPhone := map[string]bool{}
		for index, values := range matrix[1:] {
			if len(values) < 5 {
				continue
			}
			item := importRow{Row: index + 2, EmployeeNo: values[0], Name: values[1], Phone: values[2], Department: values[3], Status: values[4]}
			if item.EmployeeNo == "" && item.Name == "" && item.Phone == "" && item.Department == "" {
				continue
			}
			if item.Status == "" {
				item.Status = "ACTIVE"
			}
			rows = append(rows, item)
			if !employeeNoFormat.MatchString(item.EmployeeNo) || len(item.Name) < 1 || len(item.Name) > 100 || !phoneFormat.MatchString(item.Phone) || len(item.Department) < 1 || len(item.Department) > 100 || (item.Status != "ACTIVE" && item.Status != "FROZEN") {
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
		writeJSON(w, 200, map[string]any{"preview_id": id, "rows": rows, "errors": problems})
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
		if confirmed.Valid || time.Since(createdTime) > time.Hour {
			WriteError(w, 409, CodeConflict, "preview expired or already confirmed")
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
		for _, item := range rows {
			if invalidRows[item.Row] {
				continue
			}
			employee, password, err := employeeService.Create(r.Context(), principalFromContext(r.Context()).ID, employees.CreateInput{EmployeeNo: item.EmployeeNo, Name: item.Name, Phone: item.Phone, Department: item.Department, Status: item.Status})
			if err != nil {
				problems = append(problems, importError{item.Row, "could not create employee"})
				continue
			}
			created = append(created, map[string]any{"employee": employee, "temporary_password": password})
		}
		encodedProblems, _ := json.Marshal(problems)
		_, err = db.ExecContext(r.Context(), `UPDATE import_previews SET confirmed_at=?,errors_json=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), string(encodedProblems), id)
		if err != nil {
			unavailable(w)
			return
		}
		writeJSON(w, 200, map[string]any{"created": created, "errors": problems})
	})
	admin.HandleFunc("/api/admin/exports/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodGet(w)
			return
		}
		kind := strings.TrimPrefix(r.URL.Path, "/api/admin/exports/")
		var query string
		var header []string
		switch kind {
		case "employees":
			query = `SELECT employee_no,name,phone,department,status FROM employees ORDER BY id`
			header = []string{"employee_no", "name", "phone", "department", "status"}
		case "balances":
			query = `SELECT e.employee_no,e.name,a.balance,a.status FROM accounts a JOIN employees e ON e.id=a.employee_id ORDER BY e.id`
			header = []string{"employee_no", "name", "balance_cents", "status"}
		case "transactions":
			query = `SELECT transaction_no,type,amount,before_balance,after_balance,business_type,business_id,created_at FROM transactions ORDER BY id`
			header = []string{"transaction_no", "type", "amount_cents", "before_balance_cents", "after_balance_cents", "business_type", "business_id", "created_at"}
		case "reconciliation":
			query = `SELECT business_date,opening_cents,movement_cents,expected_cents,actual_cents,difference_cents FROM daily_reconciliation ORDER BY business_date`
			header = []string{"business_date", "opening_cents", "movement_cents", "expected_cents", "actual_cents", "difference_cents"}
		default:
			notFound(w, r)
			return
		}
		rows, err := db.QueryContext(r.Context(), query)
		if err != nil {
			unavailable(w)
			return
		}
		defer rows.Close()
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.csv"`, kind))
		w.Write([]byte{0xef, 0xbb, 0xbf})
		writer := csv.NewWriter(w)
		writer.Write(header)
		for rows.Next() {
			values := make([]any, len(header))
			dest := make([]any, len(header))
			for index := range values {
				dest[index] = &values[index]
			}
			if err := rows.Scan(dest...); err != nil {
				return
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
			writer.Write(line)
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
