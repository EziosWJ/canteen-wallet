package terminal

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/employees"
	"github.com/EziosWJ/canteen-wallet/backend/internal/ledger"
	"github.com/EziosWJ/canteen-wallet/backend/internal/paymenttokens"
)

type PresentationStatus struct {
	ID         string    `json:"presentation_id"`
	State      string    `json:"state"`
	ServerTime time.Time `json:"server_time"`
	Result     *Result   `json:"result,omitempty"`
}

type pendingRecord struct {
	ID, TerminalID, MealCode, MealName, Date, State, ResultCode                            string
	TokenID, EmployeeID, SessionID, AccountID, Amount, MealEndAt, ExpiresAt, TransactionID int64
	PresentationID                                                                         string
}

const pendingColumns = `p.id,p.terminal_id,p.meal_code,p.meal_name,p.business_date,p.state,
	COALESCE(p.result_code,''),p.token_id,p.employee_id,t.session_id,a.id,p.amount_cents,
	p.meal_end_at,p.expires_at,COALESCE(p.transaction_id,0),COALESCE(t.presentation_id,'')`

func scanPending(row *sql.Row) (pendingRecord, error) {
	return scanPendingRows(row)
}

// pendingScanner covers both *sql.Row and *sql.Rows so the pending columns can be
// read one at a time or in bulk with the same column list.
type pendingScanner interface{ Scan(dest ...any) error }

func scanPendingRows(row pendingScanner) (pendingRecord, error) {
	var p pendingRecord
	err := row.Scan(&p.ID, &p.TerminalID, &p.MealCode, &p.MealName, &p.Date, &p.State,
		&p.ResultCode, &p.TokenID, &p.EmployeeID, &p.SessionID, &p.AccountID, &p.Amount,
		&p.MealEndAt, &p.ExpiresAt, &p.TransactionID, &p.PresentationID)
	return p, err
}

func (s *Service) pendingByToken(ctx context.Context, tx *sql.Tx, tokenID int64) (pendingRecord, error) {
	return scanPending(tx.QueryRowContext(ctx, `SELECT `+pendingColumns+` FROM pending_consumptions p
		JOIN payment_tokens t ON t.id=p.token_id JOIN accounts a ON a.employee_id=p.employee_id
		WHERE p.token_id=?`, tokenID))
}

func (s *Service) pendingByID(ctx context.Context, tx *sql.Tx, id string) (pendingRecord, error) {
	return scanPending(tx.QueryRowContext(ctx, `SELECT `+pendingColumns+` FROM pending_consumptions p
		JOIN payment_tokens t ON t.id=p.token_id JOIN accounts a ON a.employee_id=p.employee_id
		WHERE p.id=?`, id))
}

func (s *Service) activePendingForMeal(ctx context.Context, tx *sql.Tx, employeeID int64, date, code string) (pendingRecord, error) {
	return scanPending(tx.QueryRowContext(ctx, `SELECT `+pendingColumns+` FROM pending_consumptions p
		JOIN payment_tokens t ON t.id=p.token_id JOIN accounts a ON a.employee_id=p.employee_id
		WHERE p.employee_id=? AND p.business_date=? AND p.meal_code=? AND p.state='PENDING'`, employeeID, date, code))
}

func (s *Service) expirePending(ctx context.Context, tx *sql.Tx, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE pending_consumptions SET state='EXPIRED',
		result_code=CASE WHEN meal_end_at<=? THEN 'MEAL_ENDED' ELSE 'PENDING_EXPIRED' END
		WHERE state='PENDING' AND expires_at<=?`, now.Unix(), now.Unix()); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE payment_presentations SET state='FAILED',
		result_code=(SELECT p.result_code FROM pending_consumptions p JOIN payment_tokens t ON t.id=p.token_id
				WHERE t.presentation_id=payment_presentations.id AND p.state='EXPIRED' ORDER BY p.rowid DESC LIMIT 1)
		WHERE state='ACTIVE' AND EXISTS(SELECT 1 FROM pending_consumptions p
			JOIN payment_tokens t ON t.id=p.token_id WHERE t.presentation_id=payment_presentations.id AND p.state='EXPIRED')`)
	return err
}

func (s *Service) pendingResult(ctx context.Context, tx *sql.Tx, pending pendingRecord, now time.Time) (Result, error) {
	if pending.State == "PENDING" && now.Unix() >= pending.ExpiresAt {
		if err := s.expirePending(ctx, tx, now); err != nil {
			return Result{}, err
		}
		var err error
		pending, err = s.pendingByID(ctx, tx, pending.ID)
		if err != nil {
			return Result{}, err
		}
	}
	if pending.State == "PROCESSED" {
		return s.processedResult(ctx, tx, pending.TransactionID)
	}
	var name string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM employees WHERE id=?`, pending.EmployeeID).Scan(&name); err != nil {
		return Result{}, err
	}
	result := Result{Status: "PENDING", Code: "CONFIRM_REQUIRED", Message: "等待员工确认再次消费", EmployeeName: name,
		MealCode: pending.MealCode, MealName: pending.MealName, AmountCents: pending.Amount,
		PendingID: pending.ID}
	if pending.State == "PENDING" {
		expires := time.Unix(pending.ExpiresAt, 0).UTC()
		result.ExpiresAt = &expires
		return result, nil
	}
	result.Status = "FAILED"
	result.Code = pending.ResultCode
	switch pending.ResultCode {
	case "CANCELLED":
		result.Message = "员工已取消再次消费"
	case "MEAL_ENDED":
		result.Message = "原餐次已结束，请重新扫码"
	case "INSUFFICIENT_FUNDS":
		result.Message = "余额不足"
	case "ACCOUNT_UNAVAILABLE":
		result.Message = "账户不可用"
	default:
		result.Message = "确认已过期"
	}
	return result, nil
}

func (s *Service) Presentation(ctx context.Context, principal employees.Principal, id string) (PresentationStatus, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PresentationStatus{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if err := s.expirePending(ctx, tx, now); err != nil {
		return PresentationStatus{}, err
	}
	var state, resultCode string
	var transactionID int64
	if id == "" {
		err = tx.QueryRowContext(ctx, `SELECT id,state,COALESCE(result_code,''),COALESCE(transaction_id,0)
			FROM payment_presentations WHERE employee_id=? AND session_id=? ORDER BY rowid DESC LIMIT 1`,
			principal.ID, principal.SessionID).Scan(&id, &state, &resultCode, &transactionID)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT state,COALESCE(result_code,''),COALESCE(transaction_id,0)
			FROM payment_presentations WHERE id=? AND employee_id=? AND session_id=?`,
			id, principal.ID, principal.SessionID).Scan(&state, &resultCode, &transactionID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return PresentationStatus{}, ErrNotFound
	}
	if err != nil {
		return PresentationStatus{}, err
	}
	status := PresentationStatus{ID: id, State: "WAITING", ServerTime: now}
	pending, pendingErr := scanPending(tx.QueryRowContext(ctx, `SELECT `+pendingColumns+` FROM pending_consumptions p
		JOIN payment_tokens t ON t.id=p.token_id JOIN accounts a ON a.employee_id=p.employee_id
		WHERE t.presentation_id=? ORDER BY p.rowid DESC LIMIT 1`, id))
	if pendingErr == nil {
		result, err := s.pendingResult(ctx, tx, pending, now)
		if err != nil {
			return PresentationStatus{}, err
		}
		status.State, status.Result = result.Status, &result
	} else if !errors.Is(pendingErr, sql.ErrNoRows) {
		return PresentationStatus{}, pendingErr
	} else if state == "SUCCESS" && transactionID > 0 {
		result, err := s.processedResult(ctx, tx, transactionID)
		if err != nil {
			return PresentationStatus{}, err
		}
		status.State, status.Result = "SUCCESS", &result
	} else if state == "FAILED" {
		var message string
		_ = tx.QueryRowContext(ctx, `SELECT COALESCE(result_message,'') FROM payment_tokens
			WHERE presentation_id=? AND result_code=? ORDER BY id DESC LIMIT 1`, id, resultCode).Scan(&message)
		result := Result{Status: "FAILED", Code: resultCode, Message: message}
		status.State, status.Result = "FAILED", &result
	}
	if err := tx.Commit(); err != nil {
		return PresentationStatus{}, err
	}
	return status, nil
}

func (s *Service) ConfirmEmployee(ctx context.Context, principal employees.Principal, pendingID string) (Result, error) {
	return s.decideEmployee(ctx, principal, pendingID, true)
}

func (s *Service) CancelEmployee(ctx context.Context, principal employees.Principal, pendingID string) (Result, error) {
	return s.decideEmployee(ctx, principal, pendingID, false)
}

func (s *Service) decideEmployee(ctx context.Context, principal employees.Principal, pendingID string, confirm bool) (Result, error) {
	if len(pendingID) != 36 || pendingID[:4] != "pnd_" {
		return Result{}, ErrInvalidRequest
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if err := s.expirePending(ctx, tx, now); err != nil {
		return Result{}, err
	}
	pending, err := s.pendingByID(ctx, tx, pendingID)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, ErrNotFound
	}
	if err != nil {
		return Result{}, err
	}
	if pending.EmployeeID != principal.ID || pending.SessionID != principal.SessionID {
		return Result{}, ErrNotFound
	}
	if pending.State != "PENDING" {
		result, err := s.pendingResult(ctx, tx, pending, now)
		if err != nil {
			return Result{}, err
		}
		return result, tx.Commit()
	}
	if !confirm {
		if err := s.finalizePending(ctx, tx, pending, "CANCELLED"); err != nil {
			return Result{}, err
		}
		result, err := s.pendingResult(ctx, tx, pendingRecordWithState(pending, "CANCELLED", "CANCELLED"), now)
		if err != nil {
			return Result{}, err
		}
		return result, tx.Commit()
	}
	var employeeStatus, accountStatus string
	var mustChange, sessionExpiry int64
	var revokedAt sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT e.status,a.status,e.must_change_password,s.expires_at,s.revoked_at
		FROM employee_sessions s JOIN employees e ON e.id=s.employee_id
		JOIN accounts a ON a.employee_id=e.id WHERE s.id=? AND s.employee_id=?`,
		pending.SessionID, pending.EmployeeID).Scan(&employeeStatus, &accountStatus, &mustChange, &sessionExpiry, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, ErrEmployeeSessionInvalid
	}
	if err != nil {
		return Result{}, err
	}
	if revokedAt.Valid || sessionExpiry <= now.Unix() {
		return Result{}, ErrEmployeeSessionInvalid
	}
	if !principal.CanIssuePaymentToken() || employeeStatus != "ACTIVE" || accountStatus != "ACTIVE" || mustChange != 0 {
		if err := s.finalizePending(ctx, tx, pending, "ACCOUNT_UNAVAILABLE"); err != nil {
			return Result{}, err
		}
		result, err := s.pendingResult(ctx, tx, pendingRecordWithState(pending, "FAILED", "ACCOUNT_UNAVAILABLE"), now)
		if err != nil {
			return Result{}, err
		}
		return result, tx.Commit()
	}
	record := paymenttokens.Record{ID: pending.TokenID, EmployeeID: pending.EmployeeID, AccountID: pending.AccountID, PresentationID: pending.PresentationID}
	result, err := s.applyConsumption(ctx, tx, record, pending.TerminalID, pending.MealCode, pending.MealName, pending.Date, pending.Amount)
	if errors.Is(err, ledger.ErrInsufficientFunds) || errors.Is(err, ledger.ErrAccountUnavailable) {
		code := "INSUFFICIENT_FUNDS"
		if errors.Is(err, ledger.ErrAccountUnavailable) {
			code = "ACCOUNT_UNAVAILABLE"
		}
		if err := s.finalizePending(ctx, tx, pending, code); err != nil {
			return Result{}, err
		}
		result, err := s.pendingResult(ctx, tx, pendingRecordWithState(pending, "FAILED", code), now)
		if err != nil {
			return Result{}, err
		}
		return result, tx.Commit()
	}
	if err != nil {
		return Result{}, err
	}
	return result, tx.Commit()
}

func pendingRecordWithState(p pendingRecord, state, code string) pendingRecord {
	p.State, p.ResultCode = state, code
	return p
}

func (s *Service) finalizePending(ctx context.Context, tx *sql.Tx, pending pendingRecord, code string) error {
	state := "FAILED"
	if code == "CANCELLED" {
		state = "CANCELLED"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pending_consumptions SET state=?,result_code=? WHERE id=? AND state='PENDING'`, state, code, pending.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE payment_tokens SET result_code=?,result_message=? WHERE id=?`, code, code, pending.TokenID); err != nil {
		return err
	}
	if pending.PresentationID != "" {
		_, err := tx.ExecContext(ctx, `UPDATE payment_presentations SET state='FAILED',result_code=? WHERE id=?`, code, pending.PresentationID)
		return err
	}
	return nil
}

func (s *Service) PendingStatus(ctx context.Context, terminalID, pendingID string) (Result, error) {
	if len(pendingID) != 36 || pendingID[:4] != "pnd_" {
		return Result{}, ErrInvalidRequest
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if err := s.expirePending(ctx, tx, now); err != nil {
		return Result{}, err
	}
	pending, err := s.pendingByID(ctx, tx, pendingID)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, ErrNotFound
	}
	if err != nil {
		return Result{}, err
	}
	if pending.TerminalID != terminalID {
		return Result{}, ErrNotFound
	}
	result, err := s.pendingResult(ctx, tx, pending, now)
	if err != nil {
		return Result{}, err
	}
	return result, tx.Commit()
}

func (s *Service) LatestPending(ctx context.Context, terminalID string) (Result, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM pending_consumptions
		WHERE terminal_id=? AND substr(created_at,1,19)>=substr(?,1,19) ORDER BY rowid DESC LIMIT 1`,
		terminalID, time.Now().UTC().Add(-2*time.Minute).Format(time.RFC3339Nano)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, ErrNotFound
	}
	if err != nil {
		return Result{}, err
	}
	return s.PendingStatus(ctx, terminalID, id)
}

// RecentPending returns all unresolved requests plus recent final decisions so
// a restarted terminal can resume every outstanding confirmation.
func (s *Service) RecentPending(ctx context.Context, terminalID string) ([]Result, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM pending_consumptions
		WHERE terminal_id=? AND (state='PENDING' OR created_at>=?) ORDER BY rowid DESC LIMIT 100`,
		terminalID, time.Now().UTC().Add(-2*time.Minute).Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(ids))
	for _, id := range ids {
		result, err := s.PendingStatus(ctx, terminalID, id)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}
