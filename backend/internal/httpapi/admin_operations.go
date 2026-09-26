package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
		cursor, _ := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
		kind := r.URL.Query().Get("type")
		if kind != "" && kind != "RECHARGE" && kind != "RECHARGE_REVERSAL" && kind != "CONSUME" && kind != "REFUND" && kind != "BALANCE_ADJUSTMENT" && kind != "BALANCE_WITHDRAWAL" {
			WriteError(w, 400, CodeInvalidRequest, "invalid transaction type")
			return
		}
		employeeID, _ := strconv.ParseInt(r.URL.Query().Get("employee_id"), 10, 64)
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
		items, err := listTransactions(r.Context(), db, cursor, limit, kind, employeeID, from, to)
		if err != nil {
			unavailable(w)
			return
		}
		next := ""
		if len(items) == limit {
			next = strconv.FormatInt(items[len(items)-1]["id"].(int64), 10)
		}
		writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
	})
	admin.HandleFunc("/api/admin/transactions/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/admin/transactions/")
		parts := strings.Split(path, "/")
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
			rows, err := db.QueryContext(r.Context(), `SELECT t.id,t.type,t.amount,t.administrator_id,COALESCE(rr.status,'PENDING'),COALESCE(rr.reviewer_id,0),COALESCE(rr.note,''),COALESCE(receipt.receipt_ref,''),COALESCE(receipt.collected_at,''),COALESCE(receipt.payment_method,'')
				FROM transactions t LEFT JOIN receipt_reviews rr ON rr.transaction_id=t.id
				LEFT JOIN recharge_receipts receipt ON receipt.recharge_transaction_id=CASE WHEN t.type='RECHARGE' THEN t.id ELSE t.related_transaction_id END
				WHERE t.type IN ('RECHARGE','RECHARGE_REVERSAL') ORDER BY t.id DESC LIMIT 200`)
			if err != nil {
				unavailable(w)
				return
			}
			defer rows.Close()
			items := make([]map[string]any, 0)
			for rows.Next() {
				var id, amount, actor, reviewer int64
				var kind, status, note, receiptRef, collectedAt, paymentMethod string
				if err := rows.Scan(&id, &kind, &amount, &actor, &status, &reviewer, &note, &receiptRef, &collectedAt, &paymentMethod); err != nil {
					unavailable(w)
					return
				}
				items = append(items, map[string]any{"transaction_id": id, "type": kind, "amount_cents": amount, "entered_by": actor, "status": status, "reviewer_id": reviewer, "note": note, "receipt_ref": receiptRef, "collected_at": collectedAt, "payment_method": paymentMethod})
			}
			if rows.Err() != nil {
				unavailable(w)
				return
			}
			writeJSON(w, 200, map[string]any{"items": items})
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

func listTransactions(ctx context.Context, db *sql.DB, cursor int64, limit int, kind string, employeeID int64, from, to string) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,account_id,type,amount,before_balance,after_balance,business_type,business_id,COALESCE(related_transaction_id,0),COALESCE(administrator_id,0),COALESCE(terminal_id,''),COALESCE(reason,''),created_at FROM transactions WHERE (?=0 OR id<?) AND (?='' OR type=?) AND (?=0 OR account_id=(SELECT id FROM accounts WHERE employee_id=?)) AND (?='' OR created_at>=?) AND (?='' OR created_at<?) ORDER BY id DESC LIMIT ?`, cursor, cursor, kind, kind, employeeID, employeeID, from, from, to, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, account, amount, before, after, related, actor int64
		var kind, businessType, businessID, terminalID, reason, created string
		if err := rows.Scan(&id, &account, &kind, &amount, &before, &after, &businessType, &businessID, &related, &actor, &terminalID, &reason, &created); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"id": id, "account_id": account, "type": kind, "amount_cents": amount, "before_balance_cents": before, "after_balance_cents": after, "business_type": businessType, "business_id": businessID, "related_transaction_id": related, "administrator_id": actor, "terminal_id": terminalID, "reason": reason, "created_at": created})
	}
	return items, rows.Err()
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
