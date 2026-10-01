package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/ledger"
	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
)

func adminOperationRoutes(admin *http.ServeMux, db *sql.DB) {
	admin.HandleFunc("/api/admin/transactions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodGet(w)
			return
		}
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 200 {
				WriteError(w, 400, CodeInvalidRequest, "invalid limit")
				return
			}
			limit = parsed
		}
		cursor := int64(0)
		if raw := r.URL.Query().Get("cursor"); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || parsed < 1 {
				WriteError(w, 400, CodeInvalidRequest, "invalid transaction cursor")
				return
			}
			cursor = parsed
		}
		kind := r.URL.Query().Get("type")
		if kind != "" && kind != "RECHARGE" && kind != "RECHARGE_REVERSAL" && kind != "CONSUME" && kind != "REFUND" && kind != "BALANCE_ADJUSTMENT" && kind != "BALANCE_WITHDRAWAL" {
			WriteError(w, 400, CodeInvalidRequest, "invalid transaction type")
			return
		}
		employeeID := int64(0)
		if raw := r.URL.Query().Get("employee_id"); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || parsed < 1 {
				WriteError(w, 400, CodeInvalidRequest, "invalid employee id")
				return
			}
			employeeID = parsed
		}
		from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
		if from != "" {
			if _, err := time.Parse("2006-01-02", from); err != nil {
				WriteError(w, 400, CodeInvalidRequest, "invalid from date")
				return
			}
			from += "T00:00:00"
		}
		if to != "" {
			if day, err := time.Parse("2006-01-02", to); err != nil {
				WriteError(w, 400, CodeInvalidRequest, "invalid to date")
				return
			} else {
				to = day.AddDate(0, 0, 1).Format("2006-01-02") + "T00:00:00"
			}
		}
		if from != "" && to != "" && from >= to {
			WriteError(w, 400, CodeInvalidRequest, "invalid transaction date range")
			return
		}
		items, hasMore, err := listTransactions(r.Context(), db, cursor, limit, kind, employeeID, from, to)
		if err != nil {
			unavailable(w)
			return
		}
		next := ""
		if hasMore {
			next = strconv.FormatInt(items[len(items)-1]["id"].(int64), 10)
		}
		writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
	})
	admin.HandleFunc("/api/admin/transactions/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/admin/transactions/")
		parts := strings.Split(path, "/")
		if len(parts) == 1 && r.Method == http.MethodGet {
			id, err := strconv.ParseInt(parts[0], 10, 64)
			if err != nil || id < 1 {
				notFound(w, r)
				return
			}
			item, err := getAdminTransaction(r.Context(), db, id)
			if errors.Is(err, sql.ErrNoRows) {
				WriteError(w, http.StatusNotFound, CodeNotFound, "transaction not found")
				return
			}
			if err != nil {
				unavailable(w)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"transaction": item})
			return
		}
		if len(parts) != 2 || parts[1] != "refund" {
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
			Reason         string `json:"reason"`
			IdempotencyKey string `json:"idempotency_key"`
		}
		if !decodeJSON(w, r, 2048, &input) {
			return
		}
		if len(strings.TrimSpace(input.Reason)) < 1 || len(input.Reason) > 512 || len(input.IdempotencyKey) < 1 || len(input.IdempotencyKey) > 128 {
			WriteError(w, 400, CodeInvalidRequest, "reason and idempotency key required")
			return
		}
		var accountID, amount int64
		var kind string
		err = db.QueryRowContext(r.Context(), `SELECT account_id,amount,type FROM transactions WHERE id=?`, id).Scan(&accountID, &amount, &kind)
		if errors.Is(err, sql.ErrNoRows) || kind != "CONSUME" {
			WriteError(w, 404, CodeNotFound, "consumption not found")
			return
		}
		if err != nil {
			unavailable(w)
			return
		}
		result, err := ledger.New(db).Apply(r.Context(), ledger.Request{AccountID: accountID, Kind: ledger.Refund, Amount: -amount, AdministratorID: principalFromContext(r.Context()).ID, BusinessType: "CONSUME", BusinessID: strconv.FormatInt(id, 10), RelatedTransactionID: id, IdempotencyKey: input.IdempotencyKey, Reason: input.Reason})
		if err != nil {
			fundError(w, err)
			return
		}
		status := 201
		if result.Replayed {
			status = 200
		}
		writeJSON(w, status, map[string]any{"transaction": fundTransactionJSON(result.Entry), "replayed": result.Replayed})
	})
	admin.HandleFunc("/api/admin/terminals", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodGet(w)
			return
		}
		rows, err := db.QueryContext(r.Context(), `SELECT id,name,enabled,COALESCE(last_seen_at,''),scanner_status,voice_status,database_status FROM terminals ORDER BY id`)
		if err != nil {
			unavailable(w)
			return
		}
		defer rows.Close()
		items := make([]map[string]any, 0)
		for rows.Next() {
			var id, name, last, scanner, voice, database string
			var enabled int
			if err := rows.Scan(&id, &name, &enabled, &last, &scanner, &voice, &database); err != nil {
				unavailable(w)
				return
			}
			items = append(items, map[string]any{"id": id, "name": name, "enabled": enabled == 1, "last_seen_at": last, "scanner_status": scanner, "voice_status": voice, "database_status": database})
		}
		if rows.Err() != nil {
			unavailable(w)
			return
		}
		writeJSON(w, 200, map[string]any{"items": items})
	})
	admin.HandleFunc("/api/admin/scan-events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodGet(w)
			return
		}
		rows, err := db.QueryContext(r.Context(), `SELECT id,terminal_id,COALESCE(employee_id,0),result_code,COALESCE(transaction_id,0),created_at FROM scan_events ORDER BY id DESC LIMIT 100`)
		if err != nil {
			unavailable(w)
			return
		}
		defer rows.Close()
		items := make([]map[string]any, 0)
		for rows.Next() {
			var id, employeeID, transactionID int64
			var terminalID, code, created string
			if err := rows.Scan(&id, &terminalID, &employeeID, &code, &transactionID, &created); err != nil {
				unavailable(w)
				return
			}
			items = append(items, map[string]any{"id": id, "terminal_id": terminalID, "employee_id": employeeID, "result_code": code, "transaction_id": transactionID, "created_at": created})
		}
		if rows.Err() != nil {
			unavailable(w)
			return
		}
		writeJSON(w, 200, map[string]any{"items": items})
	})
	admin.HandleFunc("/api/admin/audit-events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodGet(w)
			return
		}
		rows, err := db.QueryContext(r.Context(), `SELECT id,COALESCE(administrator_id,0),action,COALESCE(subject_type,''),COALESCE(subject_id,''),details_json,created_at FROM audit_events ORDER BY id DESC LIMIT 200`)
		if err != nil {
			unavailable(w)
			return
		}
		defer rows.Close()
		items := make([]map[string]any, 0)
		for rows.Next() {
			var id, actor int64
			var action, subjectType, subjectID, details, created string
			if err := rows.Scan(&id, &actor, &action, &subjectType, &subjectID, &details, &created); err != nil {
				unavailable(w)
				return
			}
			items = append(items, map[string]any{"id": id, "administrator_id": actor, "action": action, "subject_type": subjectType, "subject_id": subjectID, "details_json": details, "created_at": created})
		}
		if rows.Err() != nil {
			unavailable(w)
			return
		}
		writeJSON(w, 200, map[string]any{"items": items})
	})
	admin.HandleFunc("/api/admin/reconciliation/daily", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			date := r.URL.Query().Get("business_date")
			if date != "" {
				if _, err := time.Parse("2006-01-02", date); err != nil {
					WriteError(w, 400, CodeInvalidRequest, "invalid business date")
					return
				}
			}
			rows, err := db.QueryContext(r.Context(), `SELECT business_date,opening_cents,movement_cents,expected_cents,actual_cents,difference_cents,generated_by,generated_at,breakdown_json FROM daily_reconciliation WHERE (?='' OR business_date=?) ORDER BY business_date DESC LIMIT 100`, date, date)
			if err != nil {
				unavailable(w)
				return
			}
			defer rows.Close()
			items := make([]map[string]any, 0)
			for rows.Next() {
				var date, at, breakdownJSON string
				var opening, movement, expected, actual, difference, actor int64
				if err := rows.Scan(&date, &opening, &movement, &expected, &actual, &difference, &actor, &at, &breakdownJSON); err != nil {
					unavailable(w)
					return
				}
				breakdown := map[string]int64{}
				_ = json.Unmarshal([]byte(breakdownJSON), &breakdown)
				items = append(items, map[string]any{"business_date": date, "opening_cents": opening, "movement_cents": movement, "expected_cents": expected, "actual_cents": actual, "difference_cents": difference, "generated_by": actor, "generated_at": at, "breakdown_cents": breakdown})
			}
			if rows.Err() != nil {
				unavailable(w)
				return
			}
			writeJSON(w, 200, map[string]any{"items": items})
		case http.MethodPost:
			var input struct {
				BusinessDate string `json:"business_date"`
			}
			if !decodeJSON(w, r, 1024, &input) {
				return
			}
			_, parseErr := time.Parse("2006-01-02", input.BusinessDate)
			zone := os.Getenv("CANTEEN_TIME_ZONE")
			if zone == "" {
				zone = "Asia/Shanghai"
			}
			location, _ := time.LoadLocation(zone)
			if parseErr != nil || input.BusinessDate > time.Now().In(location).Format("2006-01-02") {
				WriteError(w, 400, CodeInvalidRequest, "invalid business date")
				return
			}
			item, err := createDaily(r.Context(), db, input.BusinessDate, principalFromContext(r.Context()).ID)
			if err != nil {
				unavailable(w)
				return
			}
			writeJSON(w, 201, item)
		default:
			methodPost(w)
		}
	})
	admin.HandleFunc("/api/admin/receipt-reviews", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			limit := 50
			if raw := r.URL.Query().Get("limit"); raw != "" {
				parsed, err := strconv.Atoi(raw)
				if err != nil || parsed < 1 || parsed > 200 {
					WriteError(w, 400, CodeInvalidRequest, "invalid limit")
					return
				}
				limit = parsed
			}
			cursor := int64(0)
			if raw := r.URL.Query().Get("cursor"); raw != "" {
				parsed, err := strconv.ParseInt(raw, 10, 64)
				if err != nil || parsed < 1 {
					WriteError(w, 400, CodeInvalidRequest, "invalid receipt review cursor")
					return
				}
				cursor = parsed
			}
			status := r.URL.Query().Get("status")
			if status == "" {
				status = "PENDING"
			}
			if status != "PENDING" && status != "MATCHED" && status != "DIFFERENCE" && status != "RESOLVED" && status != "ALL" {
				WriteError(w, 400, CodeInvalidRequest, "invalid receipt review status")
				return
			}
			employeeID := int64(0)
			if raw := r.URL.Query().Get("employee_id"); raw != "" {
				parsed, err := strconv.ParseInt(raw, 10, 64)
				if err != nil || parsed < 1 {
					WriteError(w, 400, CodeInvalidRequest, "invalid employee id")
					return
				}
				employeeID = parsed
			}
			fromDate, toDate := r.URL.Query().Get("from"), r.URL.Query().Get("to")
			if day := r.URL.Query().Get("business_date"); day != "" {
				if fromDate != "" || toDate != "" {
					WriteError(w, 400, CodeInvalidRequest, "business_date cannot be combined with from or to")
					return
				}
				fromDate, toDate = day, day
			}
			zone := os.Getenv("CANTEEN_TIME_ZONE")
			if zone == "" {
				zone = "Asia/Shanghai"
			}
			location, err := time.LoadLocation(zone)
			if err != nil {
				unavailable(w)
				return
			}
			var from, to string
			if fromDate != "" {
				day, e := time.ParseInLocation("2006-01-02", fromDate, location)
				if e != nil {
					WriteError(w, 400, CodeInvalidRequest, "invalid from date")
					return
				}
				from = day.UTC().Format(time.RFC3339Nano)
			}
			if toDate != "" {
				day, e := time.ParseInLocation("2006-01-02", toDate, location)
				if e != nil {
					WriteError(w, 400, CodeInvalidRequest, "invalid to date")
					return
				}
				to = day.AddDate(0, 0, 1).UTC().Format(time.RFC3339Nano)
			}
			if from != "" && to != "" && from >= to {
				WriteError(w, 400, CodeInvalidRequest, "invalid receipt review date range")
				return
			}
			rows, err := db.QueryContext(r.Context(), `SELECT t.id,t.transaction_no,t.type,t.amount,t.administrator_id,COALESCE(rr.status,'PENDING'),COALESCE(rr.reviewer_id,0),COALESCE(rr.note,''),COALESCE(rr.reviewed_at,''),e.id,e.employee_no,e.name,e.department,COALESCE(receipt.id,0),COALESCE(receipt.receipt_ref,''),COALESCE(receipt.collected_at,''),COALESCE(receipt.payment_method,''),COALESCE(receipt.recharge_transaction_id,0),COALESCE(t.related_transaction_id,0)
				FROM transactions t JOIN accounts a ON a.id=t.account_id JOIN employees e ON e.id=a.employee_id
				LEFT JOIN receipt_reviews rr ON rr.transaction_id=t.id
				LEFT JOIN recharge_receipts receipt ON receipt.recharge_transaction_id=CASE WHEN t.type='RECHARGE' THEN t.id ELSE t.related_transaction_id END
				WHERE t.type IN ('RECHARGE','RECHARGE_REVERSAL') AND (?=0 OR t.id<?) AND (?=0 OR e.id=?)
				AND (?='ALL' OR COALESCE(rr.status,'PENDING')=?) AND (?='' OR t.created_at>=?) AND (?='' OR t.created_at<?)
				ORDER BY t.id DESC LIMIT ?`, cursor, cursor, employeeID, employeeID, status, status, from, from, to, to, limit+1)
			if err != nil {
				unavailable(w)
				return
			}
			defer rows.Close()
			items := make([]map[string]any, 0)
			for rows.Next() {
				var id, amount, actor, reviewer, empID, receiptID, rechargeID, relatedID int64
				var number, kind, itemStatus, note, reviewedAt, employeeNo, employeeName, department, receiptRef, collectedAt, paymentMethod string
				if err := rows.Scan(&id, &number, &kind, &amount, &actor, &itemStatus, &reviewer, &note, &reviewedAt, &empID, &employeeNo, &employeeName, &department, &receiptID, &receiptRef, &collectedAt, &paymentMethod, &rechargeID, &relatedID); err != nil {
					unavailable(w)
					return
				}
				items = append(items, map[string]any{"transaction_id": id, "transaction_no": number, "type": kind, "amount_cents": amount, "entered_by": actor, "status": itemStatus, "reviewer_id": reviewer, "note": note, "reviewed_at": reviewedAt, "employee_id": empID, "employee_no": employeeNo, "employee_name": employeeName, "department": department, "receipt_id": receiptID, "receipt_ref": receiptRef, "collected_at": collectedAt, "payment_method": paymentMethod, "recharge_transaction_id": rechargeID, "related_transaction_id": relatedID})
			}
			if rows.Err() != nil {
				unavailable(w)
				return
			}
			hasMore := len(items) > limit
			if hasMore {
				items = items[:limit]
			}
			next := ""
			if hasMore {
				next = strconv.FormatInt(items[len(items)-1]["transaction_id"].(int64), 10)
			}
			writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
		case http.MethodPost:
			var input struct {
				TransactionID int64  `json:"transaction_id"`
				Status        string `json:"status"`
				Note          string `json:"note"`
			}
			if !decodeJSON(w, r, 2048, &input) {
				return
			}
			if input.TransactionID < 1 || (input.Status != "MATCHED" && input.Status != "DIFFERENCE" && input.Status != "RESOLVED") || len(input.Note) > 512 || (input.Status != "MATCHED" && strings.TrimSpace(input.Note) == "") {
				WriteError(w, 400, CodeInvalidRequest, "invalid review")
				return
			}
			actor := principalFromContext(r.Context()).ID
			tx, err := db.BeginTx(r.Context(), nil)
			if err != nil {
				unavailable(w)
				return
			}
			defer tx.Rollback()
			var enteredBy int64
			var kind string
			err = tx.QueryRowContext(r.Context(), `SELECT administrator_id,type FROM transactions WHERE id=?`, input.TransactionID).Scan(&enteredBy, &kind)
			if errors.Is(err, sql.ErrNoRows) || kind != "RECHARGE" && kind != "RECHARGE_REVERSAL" {
				WriteError(w, 404, CodeNotFound, "receipt transaction not found")
				return
			}
			if err != nil {
				unavailable(w)
				return
			}
			if enteredBy == actor {
				WriteError(w, 409, CodeConflict, "entry author cannot review own receipt")
				return
			}
			if input.Status == "RESOLVED" {
				var prior string
				err = tx.QueryRowContext(r.Context(), `SELECT status FROM receipt_reviews WHERE transaction_id=?`, input.TransactionID).Scan(&prior)
				if errors.Is(err, sql.ErrNoRows) || prior != "DIFFERENCE" {
					WriteError(w, 409, CodeConflict, "difference must be recorded before resolution")
					return
				}
				if err != nil {
					unavailable(w)
					return
				}
			} else {
				var prior string
				err = tx.QueryRowContext(r.Context(), `SELECT status FROM receipt_reviews WHERE transaction_id=?`, input.TransactionID).Scan(&prior)
				if err == nil && prior != "PENDING" {
					WriteError(w, 409, CodeConflict, "only pending receipts can be matched or marked different")
					return
				}
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					unavailable(w)
					return
				}
			}
			_, err = tx.ExecContext(r.Context(), `INSERT INTO receipt_reviews(transaction_id,reviewer_id,status,note,reviewed_at) VALUES(?,?,?,?,?) ON CONFLICT(transaction_id) DO UPDATE SET reviewer_id=excluded.reviewer_id,status=excluded.status,note=excluded.note,reviewed_at=excluded.reviewed_at`, input.TransactionID, actor, input.Status, input.Note, time.Now().UTC().Format(time.RFC3339Nano))
			if err != nil {
				unavailable(w)
				return
			}
			if err := store.RecordAudit(r.Context(), tx, actor, "RECEIPT_REVIEWED", "transaction", strconv.FormatInt(input.TransactionID, 10), map[string]any{"status": input.Status}); err != nil {
				unavailable(w)
				return
			}
			if err := tx.Commit(); err != nil {
				unavailable(w)
				return
			}
			writeJSON(w, 200, map[string]any{"transaction_id": input.TransactionID, "status": input.Status, "reviewer_id": actor, "note": input.Note})
		default:
			methodPost(w)
		}
	})
	admin.HandleFunc("/api/admin/receipt-reviews/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodPost(w)
			return
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/admin/receipt-reviews/"), 10, 64)
		if err != nil || id < 1 {
			notFound(w, r)
			return
		}
		var input struct {
			Status string `json:"status"`
			Note   string `json:"note"`
		}
		if !decodeJSON(w, r, 2048, &input) {
			return
		}
		body, _ := json.Marshal(map[string]any{"transaction_id": id, "status": input.Status, "note": input.Note})
		copy := r.Clone(r.Context())
		copy.URL.Path = "/api/admin/receipt-reviews"
		copy.Body = io.NopCloser(bytes.NewReader(body))
		copy.ContentLength = int64(len(body))
		admin.ServeHTTP(w, copy)
	})
	manualSupplyRoutes(admin, db)
}

func methodGet(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET")
	WriteError(w, 405, CodeMethodNotAllowed, "method not allowed")
}
func unavailable(w http.ResponseWriter) {
	WriteError(w, 503, CodeServiceUnavailable, "service unavailable")
}

func fundError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ledger.ErrDuplicateReference):
		WriteError(w, 409, CodeConflict, "already refunded")
	case errors.Is(err, ledger.ErrIdempotencyConflict), errors.Is(err, ledger.ErrBusinessConflict):
		WriteError(w, 409, CodeIdempotencyConflict, "transaction request conflicts")
	case errors.Is(err, ledger.ErrInsufficientFunds):
		WriteError(w, 409, CodeInsufficientFunds, "insufficient funds")
	case errors.Is(err, ledger.ErrInvalidRequest), errors.Is(err, ledger.ErrInvalidReference):
		WriteError(w, 400, CodeInvalidRequest, "invalid transaction request")
	case errors.Is(err, ledger.ErrAccountUnavailable):
		WriteError(w, 409, CodeAccountUnavailable, "account unavailable")
	default:
		unavailable(w)
	}
}

func listTransactions(ctx context.Context, db *sql.DB, cursor int64, limit int, kind string, employeeID int64, from, to string) ([]map[string]any, bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT t.id,t.transaction_no,t.account_id,t.type,t.amount,t.before_balance,t.after_balance,t.business_type,t.business_id,COALESCE(t.related_transaction_id,0),COALESCE(t.administrator_id,0),COALESCE(t.terminal_id,''),COALESCE(t.reason,''),t.created_at,
		e.id,e.employee_no,e.name,e.phone,e.department,
		COALESCE((SELECT id FROM transactions f WHERE f.type='REFUND' AND f.related_transaction_id=t.id LIMIT 1),0),
		COALESCE((SELECT id FROM transactions v WHERE v.type='RECHARGE_REVERSAL' AND v.related_transaction_id=t.id LIMIT 1),0),
		r.id,r.transaction_no,r.type,r.amount,a.status,
		COALESCE((SELECT w.id FROM transactions w JOIN payout_receipts p ON p.withdrawal_transaction_id=w.id
			WHERE w.type='BALANCE_WITHDRAWAL' AND w.account_id=t.account_id AND p.employee_id=e.id
			AND (w.related_transaction_id=t.id OR (w.related_transaction_id IS NULL AND w.id>t.id))
			ORDER BY w.id LIMIT 1),0)
		FROM transactions t JOIN accounts a ON a.id=t.account_id JOIN employees e ON e.id=a.employee_id
		LEFT JOIN transactions r ON r.id=t.related_transaction_id
		WHERE (?=0 OR t.id<?) AND (?='' OR t.type=?) AND (?=0 OR e.id=?) AND (?='' OR t.created_at>=?) AND (?='' OR t.created_at<?)
		ORDER BY t.id DESC LIMIT ?`, cursor, cursor, kind, kind, employeeID, employeeID, from, from, to, to, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit)
	for rows.Next() {
		var accountStatus string
		var withdrawalID int64
		item, err := scanAdminTransaction(rows, &accountStatus, &withdrawalID)
		if err != nil {
			return nil, false, err
		}
		item["payout_status"] = "NOT_APPLICABLE"
		item["withdrawal_transaction_id"] = nil
		if item["type"] == "REFUND" {
			if withdrawalID > 0 {
				item["payout_status"] = "PAID"
				item["withdrawal_transaction_id"] = withdrawalID
			} else if accountStatus == "CLOSED" {
				item["payout_status"] = "AVAILABLE"
			}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

type adminTransactionScanner interface{ Scan(...any) error }

func scanAdminTransaction(row adminTransactionScanner, extraDestinations ...any) (map[string]any, error) {
	var id, account, amount, before, after, related, actor int64
	var transactionNo, kind, businessType, businessID, terminalID, reason, created string
	var employeeID int64
	var employeeNo, employeeName, phone, department string
	var refundID, reversalID int64
	var relatedID sql.NullInt64
	var relatedNo, relatedType sql.NullString
	var relatedAmount sql.NullInt64
	destinations := []any{&id, &transactionNo, &account, &kind, &amount, &before, &after, &businessType, &businessID, &related, &actor, &terminalID, &reason, &created,
		&employeeID, &employeeNo, &employeeName, &phone, &department, &refundID, &reversalID,
		&relatedID, &relatedNo, &relatedType, &relatedAmount}
	destinations = append(destinations, extraDestinations...)
	if err := row.Scan(destinations...); err != nil {
		return nil, err
	}
	refundStatus, canRefund, refundReason := "NOT_APPLICABLE", false, "not_consumption"
	if kind == "CONSUME" {
		refundStatus, canRefund, refundReason = "AVAILABLE", refundID == 0, "already_refunded"
		if refundID != 0 {
			refundStatus = "REFUNDED"
		} else {
			refundReason = ""
		}
	}
	reversalStatus, canReverse, reversalReason := "NOT_APPLICABLE", false, "not_recharge"
	if kind == "RECHARGE" {
		reversalStatus, canReverse, reversalReason = "AVAILABLE", reversalID == 0, "already_reversed"
		if reversalID != 0 {
			reversalStatus = "REVERSED"
		} else {
			reversalReason = ""
		}
	}
	item := map[string]any{"id": id, "transaction_no": transactionNo, "account_id": account,
		"employee_id": employeeID, "employee_no": employeeNo, "employee_name": employeeName, "employee_phone": phone, "department": department,
		"type": kind, "amount_cents": amount, "before_balance_cents": before, "after_balance_cents": after,
		"business_type": businessType, "business_id": businessID, "related_transaction_id": related,
		"administrator_id": actor, "terminal_id": terminalID, "reason": reason, "created_at": created,
		"refund_status": refundStatus, "can_refund": canRefund, "refund_block_reason": refundReason,
		"reversal_status": reversalStatus, "can_reverse": canReverse, "reversal_block_reason": reversalReason}
	if relatedID.Valid {
		item["related_transaction"] = map[string]any{"id": relatedID.Int64, "transaction_no": relatedNo.String, "type": relatedType.String, "amount_cents": relatedAmount.Int64}
	} else {
		item["related_transaction"] = nil
	}
	return item, nil
}

func getAdminTransaction(ctx context.Context, db *sql.DB, id int64) (map[string]any, error) {
	row := db.QueryRowContext(ctx, `SELECT t.id,t.transaction_no,t.account_id,t.type,t.amount,t.before_balance,t.after_balance,t.business_type,t.business_id,COALESCE(t.related_transaction_id,0),COALESCE(t.administrator_id,0),COALESCE(t.terminal_id,''),COALESCE(t.reason,''),t.created_at,
		e.id,e.employee_no,e.name,e.phone,e.department,
		COALESCE((SELECT id FROM transactions f WHERE f.type='REFUND' AND f.related_transaction_id=t.id LIMIT 1),0),
		COALESCE((SELECT id FROM transactions v WHERE v.type='RECHARGE_REVERSAL' AND v.related_transaction_id=t.id LIMIT 1),0),
		r.id,r.transaction_no,r.type,r.amount
		FROM transactions t JOIN accounts a ON a.id=t.account_id JOIN employees e ON e.id=a.employee_id
		LEFT JOIN transactions r ON r.id=t.related_transaction_id WHERE t.id=?`, id)
	item, err := scanAdminTransaction(row)
	if err != nil {
		return nil, err
	}
	var balance, withdrawalID int64
	var accountStatus, employeeStatus string
	var detailKind, businessType string
	var mealCode, mealName, businessDate sql.NullString
	var mealAmount sql.NullInt64
	var manualMealCode, manualBusinessDate sql.NullString
	var manualMealAmount sql.NullInt64
	var receiptID, receiptAmount sql.NullInt64
	var receiptRef, collectedAt, paymentMethod sql.NullString
	var enteredByID sql.NullInt64
	var enteredByName sql.NullString
	err = db.QueryRowContext(ctx, `SELECT a.balance,a.status,e.status,t.type,t.business_type,
		d.meal_code,d.meal_name,d.business_date,d.amount_cents,
		ms.meal_code,ms.business_date,ms.amount_cents,
		rc.id,rc.receipt_ref,rc.amount_cents,rc.collected_at,rc.payment_method,
		entered.id,entered.username,
		COALESCE((SELECT w.id FROM transactions w JOIN payout_receipts p ON p.withdrawal_transaction_id=w.id
			WHERE w.type='BALANCE_WITHDRAWAL' AND w.account_id=t.account_id AND p.employee_id=e.id
			AND (w.related_transaction_id=t.id OR (w.related_transaction_id IS NULL AND w.id>t.id))
			ORDER BY w.id LIMIT 1),0)
		FROM transactions t JOIN accounts a ON a.id=t.account_id JOIN employees e ON e.id=a.employee_id
		LEFT JOIN consumption_details d ON d.transaction_id=t.id
		LEFT JOIN manual_supplies ms ON ms.transaction_id=t.id AND ms.status='POSTED' AND t.type='CONSUME' AND t.business_type='MANUAL_SUPPLY'
		LEFT JOIN recharge_receipts rc ON rc.recharge_transaction_id=CASE WHEN t.type='RECHARGE' THEN t.id WHEN t.type='RECHARGE_REVERSAL' THEN t.related_transaction_id ELSE 0 END
		LEFT JOIN administrators entered ON entered.id=t.administrator_id
		WHERE t.id=?`, id).Scan(&balance, &accountStatus, &employeeStatus, &detailKind, &businessType,
		&mealCode, &mealName, &businessDate, &mealAmount,
		&manualMealCode, &manualBusinessDate, &manualMealAmount,
		&receiptID, &receiptRef, &receiptAmount, &collectedAt, &paymentMethod,
		&enteredByID, &enteredByName, &withdrawalID)
	if err != nil {
		return nil, err
	}
	item["current_balance_cents"] = balance
	item["account_status"] = accountStatus
	item["employee_status"] = employeeStatus
	item["meal_snapshot"] = nil
	if detailKind == "CONSUME" {
		if mealCode.Valid {
			item["meal_snapshot"] = map[string]any{"source": businessType, "meal_code": mealCode.String, "meal_name": mealName.String, "business_date": businessDate.String, "amount_cents": mealAmount.Int64}
		} else if manualMealCode.Valid {
			// Manual supply records persist code/date/amount, but never stored a
			// meal name. Do not infer that historical name from current meal config.
			item["meal_snapshot"] = map[string]any{"source": businessType, "meal_code": manualMealCode.String, "meal_name": nil, "business_date": manualBusinessDate.String, "amount_cents": manualMealAmount.Int64}
		}
	}
	item["recharge_receipt"] = nil
	if receiptID.Valid {
		item["recharge_receipt"] = map[string]any{"id": receiptID.Int64, "receipt_ref": receiptRef.String, "amount_cents": receiptAmount.Int64, "collected_at": collectedAt.String, "payment_method": paymentMethod.String}
	}
	item["entered_by"] = nil
	if enteredByID.Valid {
		item["entered_by"] = map[string]any{"id": enteredByID.Int64, "username": enteredByName.String}
	}
	item["withdrawal_transaction_id"] = nil
	item["payout_status"] = "NOT_APPLICABLE"
	if detailKind == "REFUND" {
		if withdrawalID > 0 {
			item["withdrawal_transaction_id"] = withdrawalID
			item["payout_status"] = "PAID"
		} else if accountStatus == "CLOSED" {
			item["payout_status"] = "AVAILABLE"
		}
	}
	return item, nil
}

func createDaily(ctx context.Context, db *sql.DB, date string, actor int64) (map[string]any, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	zone := os.Getenv("CANTEEN_TIME_ZONE")
	if zone == "" {
		zone = "Asia/Shanghai"
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return nil, err
	}
	localDay, err := time.ParseInLocation("2006-01-02", date, location)
	if err != nil {
		return nil, err
	}
	// The stored RFC3339Nano timestamps may omit fractional seconds; compare
	// against a prefix boundary so both forms sort correctly as UTC text.
	start := localDay.UTC().Format("2006-01-02T15:04:05")
	end := localDay.AddDate(0, 0, 1).UTC().Format("2006-01-02T15:04:05")
	var opening, movement, actual int64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount),0) FROM transactions WHERE created_at < ?`, start).Scan(&opening)
	if err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount),0) FROM transactions WHERE created_at >= ? AND created_at < ?`, start, end).Scan(&movement)
	if err != nil {
		return nil, err
	}
	breakdown := map[string]int64{"RECHARGE": 0, "RECHARGE_REVERSAL": 0, "CONSUME": 0, "REFUND": 0, "BALANCE_ADJUSTMENT": 0, "BALANCE_WITHDRAWAL": 0}
	rows, err := tx.QueryContext(ctx, `SELECT type,COALESCE(SUM(amount),0) FROM transactions WHERE created_at >= ? AND created_at < ? GROUP BY type`, start, end)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var kind string
		var amount int64
		if err := rows.Scan(&kind, &amount); err != nil {
			rows.Close()
			return nil, err
		}
		breakdown[kind] = amount
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if date == time.Now().In(location).Format("2006-01-02") {
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(balance),0) FROM accounts`).Scan(&actual)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(t.after_balance),0) FROM transactions t WHERE t.id=(SELECT t2.id FROM transactions t2 WHERE t2.account_id=t.account_id AND t2.created_at < ? ORDER BY t2.id DESC LIMIT 1)`, end).Scan(&actual)
	}
	if err != nil {
		return nil, err
	}
	expected := opening + movement
	difference := actual - expected
	encoded, _ := json.Marshal(breakdown)
	_, err = tx.ExecContext(ctx, `INSERT INTO daily_reconciliation(business_date,opening_cents,movement_cents,expected_cents,actual_cents,difference_cents,generated_by,generated_at,breakdown_json) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(business_date) DO UPDATE SET opening_cents=excluded.opening_cents,movement_cents=excluded.movement_cents,expected_cents=excluded.expected_cents,actual_cents=excluded.actual_cents,difference_cents=excluded.difference_cents,generated_by=excluded.generated_by,generated_at=excluded.generated_at,breakdown_json=excluded.breakdown_json`, date, opening, movement, expected, actual, difference, actor, time.Now().UTC().Format(time.RFC3339Nano), string(encoded))
	if err != nil {
		return nil, err
	}
	if err := store.RecordAudit(ctx, tx, actor, "DAILY_RECONCILIATION_GENERATED", "business_date", date, map[string]any{"difference_cents": difference}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"business_date": date, "opening_cents": opening, "movement_cents": movement, "expected_cents": expected, "actual_cents": actual, "difference_cents": difference, "breakdown_cents": breakdown}, nil
}
