package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/ledger"
	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
)

func manualSupplyRoutes(admin *http.ServeMux, db *sql.DB) {
	admin.HandleFunc("/api/admin/manual-supplies", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			statuses, valid := manualSupplyStatuses(r.URL.Query()["status"])
			if !valid {
				WriteError(w, 400, CodeInvalidRequest, "invalid status filter")
				return
			}
			limit := 50
			if raw := r.URL.Query().Get("limit"); raw != "" {
				value, err := strconv.Atoi(raw)
				if err != nil || value < 1 || value > 200 {
					WriteError(w, 400, CodeInvalidRequest, "limit must be between 1 and 200")
					return
				}
				limit = value
			}
			var cursor int64
			if raw := r.URL.Query().Get("cursor"); raw != "" {
				value, err := strconv.ParseInt(raw, 10, 64)
				if err != nil || value < 1 {
					WriteError(w, 400, CodeInvalidRequest, "invalid cursor")
					return
				}
				cursor = value
			}

			query := `SELECT m.id,m.receipt_ref,m.employee_id,m.meal_code,m.business_date,m.amount_cents,m.status,m.note,m.created_by,COALESCE(m.resolved_by,0),COALESCE(m.transaction_id,0),m.created_at,COALESCE(m.resolved_at,''),e.name,e.employee_no,e.department,e.status,a.status,COALESCE(t.created_at,'')
				FROM manual_supplies m JOIN employees e ON e.id=m.employee_id JOIN accounts a ON a.employee_id=e.id LEFT JOIN transactions t ON t.id=m.transaction_id
				WHERE m.status IN (?,?,?) AND (?=0 OR m.id<?) ORDER BY m.id DESC LIMIT ?`
			rows, err := db.QueryContext(r.Context(), query, statuses[0], statuses[1], statuses[2], cursor, cursor, limit+1)
			if err != nil {
				unavailable(w)
				return
			}
			defer rows.Close()
			type manualSupplyItem struct {
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
				CreatedBy            int64  `json:"created_by"`
				ResolvedBy           int64  `json:"resolved_by"`
				TransactionID        int64  `json:"transaction_id"`
				CreatedAt            string `json:"created_at"`
				ResolvedAt           string `json:"resolved_at"`
				TransactionCreatedAt string `json:"transaction_created_at"`
			}
			items := make([]manualSupplyItem, 0, limit+1)
			for rows.Next() {
				var item manualSupplyItem
				if err := rows.Scan(&item.ID, &item.ReceiptRef, &item.EmployeeID, &item.MealCode, &item.BusinessDate, &item.AmountCents, &item.Status, &item.Note, &item.CreatedBy, &item.ResolvedBy, &item.TransactionID, &item.CreatedAt, &item.ResolvedAt, &item.EmployeeName, &item.EmployeeNo, &item.Department, &item.EmployeeStatus, &item.AccountStatus, &item.TransactionCreatedAt); err != nil {
					unavailable(w)
					return
				}
				item.MealName = manualSupplyMealName(item.MealCode)
				item.ExceptionReason = manualSupplyExceptionReason(item.Status, item.Note)
				items = append(items, item)
			}
			if rows.Err() != nil {
				unavailable(w)
				return
			}
			hasMore := len(items) > limit
			if hasMore {
				items = items[:limit]
			}
			nextCursor := ""
			if hasMore {
				nextCursor = strconv.FormatInt(items[len(items)-1].ID, 10)
			}
			writeJSON(w, 200, map[string]any{"items": items, "next_cursor": nextCursor, "has_more": hasMore})
		case http.MethodPost:
			var input struct {
				ReceiptRef   string `json:"receipt_ref"`
				EmployeeID   int64  `json:"employee_id"`
				MealCode     string `json:"meal_code"`
				BusinessDate string `json:"business_date"`
				AmountCents  int64  `json:"amount_cents"`
				Note         string `json:"note"`
			}
			if !decodeJSON(w, r, 2048, &input) {
				return
			}
			input.ReceiptRef = strings.TrimSpace(input.ReceiptRef)
			if input.EmployeeID < 1 || len(input.ReceiptRef) < 1 || len(input.ReceiptRef) > 128 || input.AmountCents < 1 || len(input.Note) > 512 || !validMealCode(input.MealCode) {
				WriteError(w, 400, CodeInvalidRequest, "invalid manual supply")
				return
			}
			if _, err := time.Parse("2006-01-02", input.BusinessDate); err != nil {
				WriteError(w, 400, CodeInvalidRequest, "invalid business date")
				return
			}
			tx, err := db.BeginTx(r.Context(), nil)
			if err != nil {
				unavailable(w)
				return
			}
			defer tx.Rollback()
			var id int64
			err = tx.QueryRowContext(r.Context(), `SELECT id FROM employees WHERE id=?`, input.EmployeeID).Scan(&id)
			if errors.Is(err, sql.ErrNoRows) {
				WriteError(w, 404, CodeNotFound, "employee not found")
				return
			}
			if err != nil {
				unavailable(w)
				return
			}
			actor := principalFromContext(r.Context()).ID
			result, err := tx.ExecContext(r.Context(), `INSERT INTO manual_supplies(receipt_ref,employee_id,meal_code,business_date,amount_cents,note,created_by,created_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(receipt_ref) DO NOTHING`, input.ReceiptRef, input.EmployeeID, input.MealCode, input.BusinessDate, input.AmountCents, input.Note, actor, time.Now().UTC().Format(time.RFC3339Nano))
			if err != nil {
				unavailable(w)
				return
			}
			affected, _ := result.RowsAffected()
			if affected == 0 {
				var employeeID, amount int64
				var code, date string
				err = tx.QueryRowContext(r.Context(), `SELECT id,employee_id,meal_code,business_date,amount_cents FROM manual_supplies WHERE receipt_ref=?`, input.ReceiptRef).Scan(&id, &employeeID, &code, &date, &amount)
				if err != nil {
					unavailable(w)
					return
				}
				if employeeID != input.EmployeeID || code != input.MealCode || date != input.BusinessDate || amount != input.AmountCents {
					WriteError(w, 409, CodeConflict, "receipt already used")
					return
				}
			} else {
				id, err = result.LastInsertId()
				if err != nil {
					unavailable(w)
					return
				}
				if err := store.RecordAudit(r.Context(), tx, actor, "MANUAL_SUPPLY_REGISTERED", "manual_supply", strconv.FormatInt(id, 10), map[string]any{"receipt_ref": input.ReceiptRef}); err != nil {
					unavailable(w)
					return
				}
			}
			if err := tx.Commit(); err != nil {
				unavailable(w)
				return
			}
			status := 201
			if affected == 0 {
				status = 200
			}
			writeJSON(w, status, map[string]any{"id": id, "receipt_ref": input.ReceiptRef, "status": "PENDING", "replayed": affected == 0})
		default:
			methodPost(w)
		}
	})
	admin.HandleFunc("/api/admin/manual-supplies/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/admin/manual-supplies/")
		parts := strings.Split(path, "/")
		if len(parts) != 2 || parts[1] != "post" {
			notFound(w, r)
			return
		}
		id, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || id < 1 {
			notFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			methodPost(w)
			return
		}
		var input struct {
			IdempotencyKey string `json:"idempotency_key"`
		}
		if !decodeJSON(w, r, 1024, &input) {
			return
		}
		if len(input.IdempotencyKey) < 1 || len(input.IdempotencyKey) > 128 {
			WriteError(w, 400, CodeInvalidRequest, "idempotency key required")
			return
		}
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			unavailable(w)
			return
		}
		defer tx.Rollback()
		var employeeID, amount, existingTransaction int64
		var receipt, code, date, status string
		err = tx.QueryRowContext(r.Context(), `SELECT receipt_ref,employee_id,meal_code,business_date,amount_cents,status,COALESCE(transaction_id,0) FROM manual_supplies WHERE id=?`, id).Scan(&receipt, &employeeID, &code, &date, &amount, &status, &existingTransaction)
		if errors.Is(err, sql.ErrNoRows) {
			WriteError(w, 404, CodeNotFound, "registration not found")
			return
		}
		if err != nil {
			unavailable(w)
			return
		}
		if status == "POSTED" {
			writeJSON(w, 200, map[string]any{"id": id, "status": "POSTED", "transaction_id": existingTransaction, "replayed": true})
			return
		}
		var accountID int64
		var accountStatus, employeeStatus string
		err = tx.QueryRowContext(r.Context(), `SELECT a.id,a.status,e.status FROM accounts a JOIN employees e ON e.id=a.employee_id WHERE e.id=?`, employeeID).Scan(&accountID, &accountStatus, &employeeStatus)
		if err != nil {
			unavailable(w)
			return
		}
		if accountStatus != "ACTIVE" || employeeStatus != "ACTIVE" {
			manualException(w, r, tx, id, "ACCOUNT_UNAVAILABLE")
			return
		}
		actor := principalFromContext(r.Context()).ID
		fund, err := ledger.ApplyInTx(r.Context(), tx, ledger.Request{AccountID: accountID, Kind: ledger.Consume, Amount: -amount, AdministratorID: actor, BusinessType: "MANUAL_SUPPLY", BusinessID: receipt, IdempotencyKey: input.IdempotencyKey, Reason: "故障供餐补录 " + date + " " + code})
		if errors.Is(err, ledger.ErrInsufficientFunds) {
			manualException(w, r, tx, id, "INSUFFICIENT_FUNDS")
			return
		}
		if err != nil {
			fundError(w, err)
			return
		}
		_, err = tx.ExecContext(r.Context(), `UPDATE manual_supplies SET status='POSTED',resolved_by=?,transaction_id=?,resolved_at=? WHERE id=?`, actor, fund.Entry.ID, time.Now().UTC().Format(time.RFC3339Nano), id)
		if err != nil {
			unavailable(w)
			return
		}
		if err := store.RecordAudit(r.Context(), tx, actor, "MANUAL_SUPPLY_POSTED", "manual_supply", strconv.FormatInt(id, 10), map[string]any{"transaction_id": fund.Entry.ID}); err != nil {
			unavailable(w)
			return
		}
		if err := tx.Commit(); err != nil {
			unavailable(w)
			return
		}
		writeJSON(w, 201, map[string]any{"id": id, "status": "POSTED", "transaction": fundTransactionJSON(fund.Entry), "replayed": fund.Replayed})
	})
}

func manualException(w http.ResponseWriter, r *http.Request, tx *sql.Tx, id int64, reason string) {
	_, err := tx.ExecContext(r.Context(), `UPDATE manual_supplies SET status='EXCEPTION',note=CASE WHEN note='' THEN ? ELSE note || '; ' || ? END WHERE id=?`, reason, reason, id)
	if err != nil {
		unavailable(w)
		return
	}
	if err := store.RecordAudit(r.Context(), tx, principalFromContext(r.Context()).ID, "MANUAL_SUPPLY_EXCEPTION", "manual_supply", strconv.FormatInt(id, 10), map[string]any{"reason": reason}); err != nil {
		unavailable(w)
		return
	}
	if err := tx.Commit(); err != nil {
		unavailable(w)
		return
	}
	writeJSON(w, 409, map[string]any{"id": id, "status": "EXCEPTION", "code": reason})
}

func manualSupplyStatuses(values []string) ([3]string, bool) {
	all := [3]string{"PENDING", "POSTED", "EXCEPTION"}
	if len(values) == 0 || len(values) == 1 && strings.TrimSpace(values[0]) == "" {
		return [3]string{"PENDING", "EXCEPTION", "PENDING"}, true
	}
	selected := make(map[string]bool)
	for _, value := range values {
		for _, status := range strings.Split(value, ",") {
			status = strings.ToUpper(strings.TrimSpace(status))
			if status == "ALL" {
				return all, true
			}
			if status != "PENDING" && status != "POSTED" && status != "EXCEPTION" {
				return [3]string{}, false
			}
			selected[status] = true
		}
	}
	if len(selected) == 0 || len(selected) > 3 {
		return [3]string{}, false
	}
	result := [3]string{"", "", ""}
	index := 0
	for _, status := range []string{"PENDING", "POSTED", "EXCEPTION"} {
		if selected[status] {
			result[index] = status
			index++
		}
	}
	for index < len(result) {
		result[index] = result[0]
		index++
	}
	return result, true
}

func manualSupplyMealName(code string) string {
	switch code {
	case "BREAKFAST":
		return "早餐"
	case "LUNCH":
		return "午餐"
	case "DINNER":
		return "晚餐"
	default:
		return code
	}
}

func manualSupplyExceptionReason(status, note string) string {
	if status != "EXCEPTION" {
		return ""
	}
	for _, reason := range []string{"INSUFFICIENT_FUNDS", "ACCOUNT_UNAVAILABLE"} {
		if strings.Contains(note, reason) {
			return reason
		}
	}
	return "EXCEPTION"
}

func validMealCode(code string) bool {
	return code == "BREAKFAST" || code == "LUNCH" || code == "DINNER"
}
