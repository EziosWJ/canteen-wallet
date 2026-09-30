package terminal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/ledger"
	"github.com/EziosWJ/canteen-wallet/backend/internal/meals"
	"github.com/EziosWJ/canteen-wallet/backend/internal/paymenttokens"
	"github.com/EziosWJ/canteen-wallet/backend/internal/settings"
)

var ErrUnauthorized = errors.New("terminal credential invalid")
var ErrInvalidRequest = errors.New("invalid terminal request")
var ErrNotFound = errors.New("payment state not found")
var ErrEmployeeSessionInvalid = errors.New("employee session invalid")

type Service struct {
	db       *sql.DB
	meals    *meals.Service
	location *time.Location
	modes    ModesReader
}

// ModesReader reads the enabled consumption entrances inside the caller's
// transaction, so an entrance is checked against the same snapshot as the money
// movement that follows. Service implements it in production.
type ModesReader interface {
	ModesTx(ctx context.Context, tx *sql.Tx) (settings.Modes, error)
}

func New(db *sql.DB, mealService *meals.Service, location *time.Location, modes ModesReader) *Service {
	return &Service{db: db, meals: mealService, location: location, modes: modes}
}

// entranceEnabled reports whether an entrance accepts new consumption work. A
// nil reader means no gate is configured, which only happens in tests that do
// not exercise consumption modes.
func (s *Service) entranceEnabled(ctx context.Context, tx *sql.Tx, isEnabled func(settings.Modes) bool) (bool, error) {
	if s.modes == nil {
		return true, nil
	}
	modes, err := s.modes.ModesTx(ctx, tx)
	if err != nil {
		return false, err
	}
	return isEnabled(modes), nil
}

type Result struct {
	Status           string     `json:"status"`
	Code             string     `json:"code"`
	Message          string     `json:"message"`
	EmployeeName     string     `json:"employee_name,omitempty"`
	MealCode         string     `json:"meal_code,omitempty"`
	MealName         string     `json:"meal_name,omitempty"`
	AmountCents      int64      `json:"amount_cents,omitempty"`
	TransactionID    int64      `json:"transaction_id,omitempty"`
	TransactionNo    string     `json:"transaction_no,omitempty"`
	ConsumptionNo    string     `json:"consumption_no,omitempty"`
	OccurredAt       string     `json:"occurred_at,omitempty"`
	PendingID        string     `json:"pending_id,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	Replayed         bool       `json:"replayed,omitempty"`
	DuplicateTrigger bool       `json:"duplicate_trigger,omitempty"`
}

type Event struct {
	ID         int64  `json:"id"`
	TerminalID string `json:"terminal_id"`
	ResultCode string `json:"result_code"`
	CreatedAt  string `json:"created_at"`
}

type scanContext struct {
	terminalID, token, date, mealCode string
	employeeID, tokenID, firstEventID int64
	receivedAt                        time.Time
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
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	decisionNow := time.Now().UTC()
	meta := scanContext{terminalID: terminalID, token: token, receivedAt: now}
	record, err := paymenttokens.LookupTx(ctx, tx, token)
	if errors.Is(err, paymenttokens.ErrTokenInvalid) {
		return s.commitScan(ctx, tx, meta, Result{Status: "FAILED", Code: "TOKEN_INVALID", Message: "无效就餐码"})
	}
	if err != nil {
		return Result{}, err
	}
	meta.employeeID, meta.tokenID = record.EmployeeID, record.ID
	var repeatedID int64
	var repeatedDate, repeatedMeal, repeatedJSON string
	err = tx.QueryRowContext(ctx, `SELECT id,business_date,meal_code,result_json FROM scan_events
		WHERE payment_token_id=? AND first_event_id IS NULL AND business_date IS NOT NULL
		AND COALESCE(json_extract(result_json,'$.replayed'),0)=0
		AND received_at_ms>? AND received_at_ms<?
		ORDER BY id DESC LIMIT 1`, record.ID, now.Add(-3*time.Second).UnixMilli(), now.Add(3*time.Second).UnixMilli()).
		Scan(&repeatedID, &repeatedDate, &repeatedMeal, &repeatedJSON)
	if err == nil {
		var result Result
		if err := json.Unmarshal([]byte(repeatedJSON), &result); err != nil {
			return Result{}, err
		}
		result.Replayed, result.DuplicateTrigger = true, true
		meta.date, meta.mealCode, meta.firstEventID = repeatedDate, repeatedMeal, repeatedID
		return s.commitScan(ctx, tx, meta, result)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, err
	}
	if record.State == "PROCESSED" {
		result, err := s.processedResult(ctx, tx, record.TransactionID)
		if err != nil {
			return Result{}, err
		}
		result.Replayed = true
		return s.commitScan(ctx, tx, meta, result)
	}
	if pending, err := s.pendingByToken(ctx, tx, record.ID); err == nil {
		result, err := s.pendingResult(ctx, tx, pending, decisionNow)
		if err != nil {
			return Result{}, err
		}
		result.Replayed = true
		meta.date, meta.mealCode = pending.Date, pending.MealCode
		return s.commitScan(ctx, tx, meta, result)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, err
	}
	if record.ResultCode != "" {
		return s.commitScan(ctx, tx, meta, Result{Status: "FAILED", Code: record.ResultCode, Message: record.ResultMessage, Replayed: true})
	}
	if record.PresentationID != "" {
		var presentationState, presentationCode string
		var presentationTransaction int64
		err := tx.QueryRowContext(ctx, `SELECT state,COALESCE(result_code,''),COALESCE(transaction_id,0)
			FROM payment_presentations WHERE id=?`, record.PresentationID).
			Scan(&presentationState, &presentationCode, &presentationTransaction)
		if err != nil {
			return Result{}, err
		}
		if presentationState == "SUCCESS" && presentationTransaction > 0 {
			result, err := s.processedResult(ctx, tx, presentationTransaction)
			if err != nil {
				return Result{}, err
			}
			result.Replayed = true
			return s.commitScan(ctx, tx, meta, result)
		}
		if presentationState == "FAILED" {
			return s.commitScan(ctx, tx, meta, Result{Status: "FAILED", Code: presentationCode,
				Message: "本次就餐码出示已结束，请重新出示", Replayed: true})
		}
	}
	if err := record.FirstUseAllowed(now); err != nil {
		return s.commitScan(ctx, tx, meta, Result{Status: "FAILED", Code: "TOKEN_UNAVAILABLE", Message: "就餐码已过期或账户不可用"})
	}
	// A disabled payment-code entrance refuses new consumption work, but only
	// after every replay branch above, so completed consumptions, recorded
	// results and status lookups keep answering with their original outcome.
	enabled, err := s.entranceEnabled(ctx, tx, func(m settings.Modes) bool { return m.PaymentCode })
	if err != nil {
		return Result{}, err
	}
	if !enabled {
		return s.failScan(ctx, tx, meta, record, Result{Status: "FAILED", Code: "MODE_DISABLED", Message: "就餐码消费已关闭"})
	}
	period, err := s.meals.ActiveAtTx(ctx, tx, now)
	if errors.Is(err, meals.ErrNoActivePeriod) {
		return s.failScan(ctx, tx, meta, record, Result{Status: "FAILED", Code: "NO_MEAL", Message: "当前无可用餐次"})
	}
	if err != nil {
		return Result{}, err
	}
	meta.date = now.In(s.location).Format("2006-01-02")
	meta.mealCode = period.Code
	if err := s.expirePending(ctx, tx, decisionNow); err != nil {
		return Result{}, err
	}
	var firstID int64
	var firstJSON string
	err = tx.QueryRowContext(ctx, `SELECT id,result_json FROM scan_events
		WHERE employee_id=? AND business_date=? AND meal_code=? AND first_event_id IS NULL
		AND COALESCE(json_extract(result_json,'$.replayed'),0)=0
		AND received_at_ms>? AND received_at_ms<? AND result_json IS NOT NULL
		ORDER BY id DESC LIMIT 1`, record.EmployeeID, meta.date, period.Code, now.Add(-3*time.Second).UnixMilli(), now.Add(3*time.Second).UnixMilli()).Scan(&firstID, &firstJSON)
	if err == nil {
		var result Result
		if err := json.Unmarshal([]byte(firstJSON), &result); err != nil {
			return Result{}, err
		}
		if record.PresentationID != "" {
			switch result.Status {
			case "SUCCESS":
				if _, err := tx.ExecContext(ctx, `UPDATE payment_presentations SET state='SUCCESS',result_code=?,transaction_id=?
					WHERE id=? AND state='ACTIVE'`, result.Code, result.TransactionID, record.PresentationID); err != nil {
					return Result{}, err
				}
			case "FAILED":
				if _, err := tx.ExecContext(ctx, `UPDATE payment_presentations SET state='FAILED',result_code=?
					WHERE id=? AND state='ACTIVE'`, result.Code, record.PresentationID); err != nil {
					return Result{}, err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE payment_tokens SET result_code=?,result_message=?
					WHERE id=? AND result_code IS NULL`, result.Code, result.Message, record.ID); err != nil {
					return Result{}, err
				}
			}
		}
		result.Replayed, result.DuplicateTrigger = true, true
		meta.firstEventID = firstID
		return s.commitScan(ctx, tx, meta, result)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, err
	}
	if pending, err := s.activePendingForMeal(ctx, tx, record.EmployeeID, meta.date, period.Code); err == nil {
		result, err := s.pendingResult(ctx, tx, pending, decisionNow)
		if err != nil {
			return Result{}, err
		}
		result.Replayed = true
		return s.commitScan(ctx, tx, meta, result)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, err
	}
	var previous int
	err = consumptionCount(ctx, tx, record.AccountID, meta.date, period.Code, &previous)
	if err != nil {
		return Result{}, err
	}
	if previous > 0 {
		endAt, err := mealEnd(now.In(s.location), period.EndTime)
		if err != nil {
			return Result{}, err
		}
		expires := now.Add(60 * time.Second)
		if endAt.Before(expires) {
			expires = endAt
		}
		pendingID, err := randomID("pnd_")
		if err != nil {
			return Result{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO pending_consumptions
			(id,token_id,terminal_id,employee_id,meal_code,meal_name,business_date,amount_cents,meal_end_at,expires_at,created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`, pendingID, record.ID, terminalID, record.EmployeeID, period.Code,
			period.Name, meta.date, period.PriceCents, endAt.Unix(), expires.Unix(), now.Format(time.RFC3339Nano))
		if err != nil {
			return Result{}, err
		}
		var name string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM employees WHERE id=?`, record.EmployeeID).Scan(&name); err != nil {
			return Result{}, err
		}
		result := Result{Status: "PENDING", Code: "CONFIRM_REQUIRED", Message: "等待员工确认再次消费", EmployeeName: name,
			MealCode: period.Code, MealName: period.Name, AmountCents: period.PriceCents, PendingID: pendingID, ExpiresAt: &expires}
		return s.commitScan(ctx, tx, meta, result)
	}
	result, err := s.applyConsumption(ctx, tx, record, terminalID, period.Code, period.Name, meta.date, period.PriceCents)
	if err != nil {
		if errors.Is(err, ledger.ErrInsufficientFunds) {
			return s.failScan(ctx, tx, meta, record, Result{Status: "FAILED", Code: "INSUFFICIENT_FUNDS", Message: "余额不足"})
		}
		if errors.Is(err, ledger.ErrAccountUnavailable) {
			return s.failScan(ctx, tx, meta, record, Result{Status: "FAILED", Code: "ACCOUNT_UNAVAILABLE", Message: "账户不可用"})
		}
		return Result{}, err
	}
	return s.commitScan(ctx, tx, meta, result)
}

func mealEnd(local time.Time, end string) (time.Time, error) {
	if len(end) != 5 {
		return time.Time{}, fmt.Errorf("invalid meal end time")
	}
	var hour, minute int
	if _, err := fmt.Sscanf(end, "%02d:%02d", &hour, &minute); err != nil {
		return time.Time{}, err
	}
	return time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, local.Location()).UTC(), nil
}

func (s *Service) failScan(ctx context.Context, tx *sql.Tx, meta scanContext, record paymenttokens.Record, result Result) (Result, error) {
	if _, err := tx.ExecContext(ctx, `UPDATE payment_tokens SET result_code=?,result_message=? WHERE id=? AND result_code IS NULL`, result.Code, result.Message, record.ID); err != nil {
		return Result{}, err
	}
	if record.PresentationID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE payment_presentations SET state='FAILED',result_code=? WHERE id=? AND state='ACTIVE'`, result.Code, record.PresentationID); err != nil {
			return Result{}, err
		}
	}
	return s.commitScan(ctx, tx, meta, result)
}

func (s *Service) applyConsumption(ctx context.Context, tx *sql.Tx, record paymenttokens.Record, terminalID, code, name, date string, amount int64) (Result, error) {
	result, err := ledger.ApplyInTx(ctx, tx, ledger.Request{AccountID: record.AccountID, Kind: ledger.Consume, Amount: -amount,
		TerminalID: terminalID, BusinessType: "MEAL_PERIOD", BusinessID: date + "|" + code,
		IdempotencyKey: fmt.Sprintf("terminal:%d", record.ID)})
	if err != nil {
		return Result{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO consumption_details(transaction_id,meal_code,meal_name,business_date,amount_cents)
		VALUES (?,?,?,?,?)`, result.Entry.ID, code, name, date, amount); err != nil {
		return Result{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE payment_tokens SET transaction_id=?,state=CASE WHEN state='ACTIVE' THEN 'PROCESSED' ELSE state END WHERE id=?`, result.Entry.ID, record.ID); err != nil {
		return Result{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pending_consumptions SET state='PROCESSED',transaction_id=?,result_code='CONSUMED' WHERE token_id=? AND state='PENDING'`, result.Entry.ID, record.ID); err != nil {
		return Result{}, err
	}
	if record.PresentationID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE payment_presentations SET state='SUCCESS',result_code='CONSUMED',transaction_id=? WHERE id=?`, result.Entry.ID, record.PresentationID); err != nil {
			return Result{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE payment_tokens SET state='REVOKED'
			WHERE presentation_id=? AND id!=? AND state='ACTIVE'`, record.PresentationID, record.ID); err != nil {
			return Result{}, err
		}
	}
	var employeeName string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM employees WHERE id=?`, record.EmployeeID).Scan(&employeeName); err != nil {
		return Result{}, err
	}
	return Result{Status: "SUCCESS", Code: "CONSUMED", Message: "消费成功", EmployeeName: employeeName,
		MealCode: code, MealName: name, AmountCents: amount, TransactionID: result.Entry.ID,
		TransactionNo: result.Entry.TransactionNo, ConsumptionNo: fmt.Sprintf("C%010d", result.Entry.ID),
		OccurredAt: result.Entry.CreatedAt.Format(time.RFC3339Nano)}, nil
}

func (s *Service) processedResult(ctx context.Context, tx *sql.Tx, transactionID int64) (Result, error) {
	var name, businessID, transactionNo, createdAt string
	var mealName, mealCode sql.NullString
	var amount int64
	err := tx.QueryRowContext(ctx, `SELECT e.name,t.business_id,-t.amount,t.transaction_no,t.created_at,
		d.meal_name,d.meal_code FROM transactions t JOIN accounts a ON a.id=t.account_id
		JOIN employees e ON e.id=a.employee_id LEFT JOIN consumption_details d ON d.transaction_id=t.id WHERE t.id=?`, transactionID).
		Scan(&name, &businessID, &amount, &transactionNo, &createdAt, &mealName, &mealCode)
	if err != nil {
		return Result{}, err
	}
	code := mealCode.String
	if code == "" {
		parts := strings.SplitN(businessID, "|", 2)
		if len(parts) == 2 {
			code = parts[1]
		}
	}
	if !mealName.Valid {
		mealName.String = code
	}
	return Result{Status: "SUCCESS", Code: "CONSUMED", Message: "消费成功", EmployeeName: name, MealCode: code,
		MealName: mealName.String, AmountCents: amount, TransactionID: transactionID,
		TransactionNo: transactionNo, ConsumptionNo: fmt.Sprintf("C%010d", transactionID), OccurredAt: createdAt}, nil
}

func (s *Service) commitScan(ctx context.Context, tx *sql.Tx, meta scanContext, result Result) (Result, error) {
	fingerprint := ""
	if meta.token != "" {
		sum := sha256.Sum256([]byte(meta.token))
		fingerprint = hex.EncodeToString(sum[:8])
	}
	var employee, tokenID, firstEvent any
	if meta.employeeID > 0 {
		employee = meta.employeeID
	}
	if meta.tokenID > 0 {
		tokenID = meta.tokenID
	}
	if meta.firstEventID > 0 {
		firstEvent = meta.firstEventID
	}
	var transaction any
	if result.TransactionID > 0 {
		transaction = result.TransactionID
	}
	var date, mealCode any
	if meta.date != "" {
		date = meta.date
	}
	if meta.mealCode != "" {
		mealCode = meta.mealCode
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return Result{}, err
	}
	eventCode := result.Code
	if result.DuplicateTrigger {
		eventCode = "DUPLICATE_TRIGGER"
	} else if result.Replayed {
		eventCode = "TOKEN_REPLAY"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO scan_events
		(terminal_id,token_fingerprint,employee_id,result_code,transaction_id,created_at,payment_token_id,business_date,meal_code,received_at_ms,result_json,first_event_id)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, meta.terminalID, fingerprint, employee, eventCode, transaction,
		meta.receivedAt.Format(time.RFC3339Nano), tokenID, date, mealCode, meta.receivedAt.UnixMilli(), string(encoded), firstEvent)
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
	items := make([]Event, 0)
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.TerminalID, &event.ResultCode, &event.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, event)
	}
	return items, rows.Err()
}

func randomID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}
