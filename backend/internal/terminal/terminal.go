package terminal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/ledger"
	"github.com/EziosWJ/canteen-wallet/backend/internal/meals"
	"github.com/EziosWJ/canteen-wallet/backend/internal/paymenttokens"
)

var ErrUnauthorized = errors.New("terminal credential invalid")
var ErrInvalidRequest = errors.New("invalid terminal request")
var ErrPendingExpired = errors.New("pending consumption expired")

type Service struct {
	db       *sql.DB
	meals    *meals.Service
	location *time.Location
}

func New(db *sql.DB, mealService *meals.Service, location *time.Location) *Service {
	return &Service{db: db, meals: mealService, location: location}
}

type Result struct {
	Status        string     `json:"status"`
	Code          string     `json:"code"`
	Message       string     `json:"message"`
	EmployeeName  string     `json:"employee_name,omitempty"`
	MealCode      string     `json:"meal_code,omitempty"`
	AmountCents   int64      `json:"amount_cents,omitempty"`
	TransactionID int64      `json:"transaction_id,omitempty"`
	PendingID     string     `json:"pending_id,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	Replayed      bool       `json:"replayed,omitempty"`
}

type Event struct {
	ID         int64  `json:"id"`
	TerminalID string `json:"terminal_id"`
	ResultCode string `json:"result_code"`
	CreatedAt  string `json:"created_at"`
}

func Provision(ctx context.Context, db *sql.DB, id, name string) (string, error) {
	if len(id) < 1 || len(id) > 64 || strings.TrimSpace(id) != id || len(name) < 1 || len(name) > 100 {
		return "", ErrInvalidRequest
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	credential := "trm_" + base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(credential))
	_, err := db.ExecContext(ctx, `INSERT INTO terminals(id, credential_hash, name, created_at) VALUES (?, ?, ?, ?)`, id, hash[:], name, time.Now().UTC().Format(time.RFC3339Nano))
	return credential, err
}

func (s *Service) Authenticate(ctx context.Context, credential string) (string, error) {
	if len(credential) != 47 || !strings.HasPrefix(credential, "trm_") {
		return "", ErrUnauthorized
	}
	raw, err := base64.RawURLEncoding.DecodeString(credential[4:])
	if err != nil || len(raw) != 32 {
		return "", ErrUnauthorized
	}
	hash := sha256.Sum256([]byte(credential))
	var id string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM terminals WHERE credential_hash = ? AND enabled = 1`, hash[:]).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUnauthorized
	}
	return id, err
}

func (s *Service) Heartbeat(ctx context.Context, id, scanner, voice string) error {
	if len(scanner) > 32 || len(voice) > 32 || scanner == "" || voice == "" {
		return ErrInvalidRequest
	}
	_, err := s.db.ExecContext(ctx, `UPDATE terminals SET last_seen_at = ?, scanner_status = ?, voice_status = ?, database_status='READY' WHERE id = ?`, time.Now().UTC().Format(time.RFC3339Nano), scanner, voice, id)
	return err
}

func (s *Service) Scan(ctx context.Context, terminalID, token string) (Result, error) {
	if len(token) > 128 {
		return Result{}, ErrInvalidRequest
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	record, lookupErr := paymenttokens.LookupTx(ctx, tx, token)
	if lookupErr != nil {
		if errors.Is(lookupErr, paymenttokens.ErrTokenInvalid) {
			result := Result{Status: "FAILED", Code: "TOKEN_INVALID", Message: "无效就餐码"}
			return s.commitEvent(ctx, tx, terminalID, token, 0, result)
		}
		return Result{}, lookupErr
	}
	if record.State == "PROCESSED" {
		result, err := s.processedResult(ctx, tx, record, true)
		if err != nil {
			return Result{}, err
		}
		return s.commitEvent(ctx, tx, terminalID, token, record.EmployeeID, result)
	}
	var pendingID string
	var pendingExpiry int64
	var pendingState, pendingMealCode string
	var pendingAmount int64
	err = tx.QueryRowContext(ctx, `SELECT id, expires_at, state, meal_code, amount_cents FROM pending_consumptions WHERE token_id = ?`, record.ID).Scan(&pendingID, &pendingExpiry, &pendingState, &pendingMealCode, &pendingAmount)
	if err == nil {
		if pendingState == "PENDING" && now.Unix() < pendingExpiry {
			expires := time.Unix(pendingExpiry, 0).UTC()
			var name string
			if err := tx.QueryRowContext(ctx, `SELECT name FROM employees WHERE id=?`, record.EmployeeID).Scan(&name); err != nil {
				return Result{}, err
			}
			return s.commitEvent(ctx, tx, terminalID, token, record.EmployeeID, Result{Status: "PENDING", Code: "CONFIRM_REQUIRED", Message: "同餐次再次消费，请确认", EmployeeName: name, MealCode: pendingMealCode, AmountCents: pendingAmount, PendingID: pendingID, ExpiresAt: &expires, Replayed: true})
		}
		result := Result{Status: "FAILED", Code: "PENDING_EXPIRED", Message: "确认已过期"}
		return s.commitEvent(ctx, tx, terminalID, token, record.EmployeeID, result)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, err
	}
	if err := record.FirstUseAllowed(now); err != nil {
		result := Result{Status: "FAILED", Code: "TOKEN_UNAVAILABLE", Message: "就餐码已过期或账户不可用"}
		return s.commitEvent(ctx, tx, terminalID, token, record.EmployeeID, result)
	}
	period, err := s.meals.ActiveAtTx(ctx, tx, now)
	if errors.Is(err, meals.ErrNoActivePeriod) {
		return s.commitEvent(ctx, tx, terminalID, token, record.EmployeeID, Result{Status: "FAILED", Code: "NO_MEAL", Message: "当前无可用餐次"})
	}
	if err != nil {
		return Result{}, err
	}
	date := now.In(s.location).Format("2006-01-02")
	var previous int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transactions t WHERE t.account_id = ? AND t.type = 'CONSUME'
		AND (t.business_id = ? OR (t.business_type = 'MANUAL_SUPPLY' AND EXISTS
			(SELECT 1 FROM manual_supplies m WHERE m.receipt_ref = t.business_id AND m.business_date = ? AND m.meal_code = ?)))
		AND NOT EXISTS (SELECT 1 FROM transactions r WHERE r.type = 'REFUND' AND r.related_transaction_id = t.id)`, record.AccountID, date+"|"+period.Code, date, period.Code).Scan(&previous)
	if err != nil {
		return Result{}, err
	}
	if previous > 0 {
		pendingID, err := randomID("pnd_")
		if err != nil {
			return Result{}, err
		}
		expires := now.Add(30 * time.Second)
		if record.ExpiresAt.Before(expires) {
			expires = record.ExpiresAt
		}
		if record.SessionExpiresAt.Before(expires) {
			expires = record.SessionExpiresAt
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO pending_consumptions(id, token_id, terminal_id, meal_code, business_date, amount_cents, expires_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, pendingID, record.ID, terminalID, period.Code, date, period.PriceCents, expires.Unix(), now.Format(time.RFC3339Nano))
		if err != nil {
			return Result{}, err
		}
		var name string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM employees WHERE id=?`, record.EmployeeID).Scan(&name); err != nil {
			return Result{}, err
		}
		result := Result{Status: "PENDING", Code: "CONFIRM_REQUIRED", Message: "同餐次再次消费，请确认", EmployeeName: name, MealCode: period.Code, AmountCents: period.PriceCents, PendingID: pendingID, ExpiresAt: &expires}
		return s.commitEvent(ctx, tx, terminalID, token, record.EmployeeID, result)
	}
	return s.consume(ctx, tx, terminalID, token, record, period.Code, date, period.PriceCents)
}

func (s *Service) Confirm(ctx context.Context, terminalID, pendingID string) (Result, error) {
	if len(pendingID) < 5 || len(pendingID) > 64 {
		return Result{}, ErrInvalidRequest
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	var tokenID, amount, expiry, transactionID int64
	var owner, code, date, state string
	err = tx.QueryRowContext(ctx, `SELECT token_id, terminal_id, meal_code, business_date, amount_cents, expires_at, state, COALESCE(transaction_id, 0)
		FROM pending_consumptions WHERE id = ?`, pendingID).Scan(&tokenID, &owner, &code, &date, &amount, &expiry, &state, &transactionID)
	if errors.Is(err, sql.ErrNoRows) || owner != terminalID {
		return Result{}, ErrInvalidRequest
	}
	if err != nil {
		return Result{}, err
	}
	if state == "PROCESSED" {
		var record paymenttokens.Record
		record.TransactionID = transactionID
		result, err := s.processedResult(ctx, tx, record, true)
		if err != nil {
			return Result{}, err
		}
		var employeeID int64
		if err := tx.QueryRowContext(ctx, `SELECT employee_id FROM payment_tokens WHERE id=?`, tokenID).Scan(&employeeID); err != nil {
			return Result{}, err
		}
		return s.commitEvent(ctx, tx, terminalID, "", employeeID, result)
	}
	if state != "PENDING" || time.Now().Unix() >= expiry {
		if state == "PENDING" {
			if _, err := tx.ExecContext(ctx, `UPDATE pending_consumptions SET state='EXPIRED' WHERE id=?`, pendingID); err != nil {
				return Result{}, err
			}
		}
		return s.commitEvent(ctx, tx, terminalID, "", 0, Result{Status: "FAILED", Code: "PENDING_EXPIRED", Message: "确认已过期"})
	}
	var accountID, employeeID int64
	var tokenState, employeeStatus, accountStatus string
	var sessionExpiry, tokenExpiry int64
	var revoked sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT p.employee_id, a.id, p.state, e.status, a.status, s.expires_at, p.expires_at, s.revoked_at
		FROM payment_tokens p JOIN employees e ON e.id=p.employee_id JOIN accounts a ON a.employee_id=e.id
		JOIN employee_sessions s ON s.id=p.session_id WHERE p.id=?`, tokenID).Scan(&employeeID, &accountID, &tokenState, &employeeStatus, &accountStatus, &sessionExpiry, &tokenExpiry, &revoked)
	if err != nil {
		return Result{}, err
	}
	if tokenState != "ACTIVE" || revoked.Valid || sessionExpiry <= time.Now().Unix() || tokenExpiry <= time.Now().Unix() || employeeStatus != "ACTIVE" || accountStatus != "ACTIVE" {
		return s.commitEvent(ctx, tx, terminalID, "", employeeID, Result{Status: "FAILED", Code: "TOKEN_UNAVAILABLE", Message: "就餐码或账户不可用"})
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pending_consumptions SET state='EXPIRED' WHERE id=? AND state='PENDING' AND expires_at <= ?`, pendingID, time.Now().Unix()); err != nil {
		return Result{}, err
	}
	record := paymenttokens.Record{ID: tokenID, EmployeeID: employeeID, AccountID: accountID}
	result, err := s.consume(ctx, tx, terminalID, "", record, code, date, amount)
	if err != nil {
		return Result{}, err
	}
	if result.Status == "SUCCESS" {
		// consume committed the transaction; the pending state must be committed with it.
		return result, nil
	}
	return result, nil
}

func (s *Service) consume(ctx context.Context, tx *sql.Tx, terminalID, token string, record paymenttokens.Record, code, date string, amount int64) (Result, error) {
	result, err := ledger.ApplyInTx(ctx, tx, ledger.Request{AccountID: record.AccountID, Kind: ledger.Consume, Amount: -amount,
		TerminalID: terminalID, BusinessType: "MEAL_PERIOD", BusinessID: date + "|" + code, IdempotencyKey: fmt.Sprintf("terminal:%d", record.ID)})
	if errors.Is(err, ledger.ErrInsufficientFunds) {
		return s.commitEvent(ctx, tx, terminalID, token, record.EmployeeID, Result{Status: "FAILED", Code: "INSUFFICIENT_FUNDS", Message: "余额不足"})
	}
	if errors.Is(err, ledger.ErrAccountUnavailable) {
		return s.commitEvent(ctx, tx, terminalID, token, record.EmployeeID, Result{Status: "FAILED", Code: "ACCOUNT_UNAVAILABLE", Message: "账户不可用"})
	}
	if err != nil {
		return Result{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE payment_tokens SET state='PROCESSED', transaction_id=? WHERE id=? AND state='ACTIVE'`, result.Entry.ID, record.ID)
	if err != nil {
		return Result{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE pending_consumptions SET state='PROCESSED', transaction_id=? WHERE token_id=? AND state='PENDING'`, result.Entry.ID, record.ID)
	if err != nil {
		return Result{}, err
	}
	var name string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM employees WHERE id=?`, record.EmployeeID).Scan(&name); err != nil {
		return Result{}, err
	}
	response := Result{Status: "SUCCESS", Code: "CONSUMED", Message: "消费成功", EmployeeName: name, MealCode: code, AmountCents: amount, TransactionID: result.Entry.ID}
	return s.commitEvent(ctx, tx, terminalID, token, record.EmployeeID, response)
}

func (s *Service) processedResult(ctx context.Context, tx *sql.Tx, record paymenttokens.Record, replay bool) (Result, error) {
	var name, businessID string
	var amount int64
	err := tx.QueryRowContext(ctx, `SELECT e.name, t.business_id, -t.amount FROM transactions t JOIN accounts a ON a.id=t.account_id
		JOIN employees e ON e.id=a.employee_id WHERE t.id=?`, record.TransactionID).Scan(&name, &businessID, &amount)
	if err != nil {
		return Result{}, err
	}
	parts := strings.SplitN(businessID, "|", 2)
	code := ""
	if len(parts) == 2 {
		code = parts[1]
	}
	return Result{Status: "SUCCESS", Code: "CONSUMED", Message: "消费成功", EmployeeName: name, MealCode: code, AmountCents: amount, TransactionID: record.TransactionID, Replayed: replay}, nil
}

func (s *Service) commitEvent(ctx context.Context, tx *sql.Tx, terminalID, token string, employeeID int64, result Result) (Result, error) {
	fingerprint := ""
	if token != "" {
		sum := sha256.Sum256([]byte(token))
		fingerprint = hex.EncodeToString(sum[:8])
	}
	var employee any
	if employeeID > 0 {
		employee = employeeID
	}
	var transaction any
	if result.TransactionID > 0 {
		transaction = result.TransactionID
	}
	eventCode := result.Code
	if result.Replayed {
		eventCode = "TOKEN_REPLAY"
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO scan_events(terminal_id,token_fingerprint,employee_id,result_code,transaction_id,created_at)
		VALUES (?,?,?,?,?,?)`, terminalID, fingerprint, employee, eventCode, transaction, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return Result{}, err
	}
	return result, tx.Commit()
}

func (s *Service) Events(ctx context.Context, terminalID string, afterID int64) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,terminal_id,result_code,created_at FROM scan_events WHERE terminal_id=? AND id>? ORDER BY id DESC LIMIT 20`, terminalID, afterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Event, 0)
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.TerminalID, &event.ResultCode, &event.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func randomID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}
