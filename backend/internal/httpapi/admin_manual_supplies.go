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
			rows, err := db.QueryContext(r.Context(), `SELECT id,receipt_ref,employee_id,meal_code,business_date,amount_cents,status,note,created_by,COALESCE(resolved_by,0),COALESCE(transaction_id,0),created_at FROM manual_supplies ORDER BY id DESC LIMIT 200`)
			if err != nil {
				unavailable(w)
				return
			}
			defer rows.Close()
			items := make([]map[string]any, 0)
			for rows.Next() {
				var id, employeeID, amount, creator, resolver, transactionID int64
				var ref, code, date, status, note, created string
				if err := rows.Scan(&id, &ref, &employeeID, &code, &date, &amount, &status, &note, &creator, &resolver, &transactionID, &created); err != nil {
					unavailable(w)
					return
				}
				items = append(items, map[string]any{"id": id, "receipt_ref": ref, "employee_id": employeeID, "meal_code": code, "business_date": date, "amount_cents": amount, "status": status, "note": note, "created_by": creator, "resolved_by": resolver, "transaction_id": transactionID, "created_at": created})
			}
			if rows.Err() != nil {
				unavailable(w)
				return
			}
			writeJSON(w, 200, map[string]any{"items": items})
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

func validMealCode(code string) bool {
	return code == "BREAKFAST" || code == "LUNCH" || code == "DINNER"
}
