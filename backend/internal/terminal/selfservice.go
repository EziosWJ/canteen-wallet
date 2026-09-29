package terminal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/employees"
	"github.com/EziosWJ/canteen-wallet/backend/internal/ledger"
	"github.com/EziosWJ/canteen-wallet/backend/internal/meals"
	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
)

// SelfServicePreview is everything the employee must see before confirming. It
// is produced only by the server: meal, business date, price and the existing
// consumption count are decided from server time and current configuration, and
// IntentID is the server credential bound to this employee and this intent.
// A client cannot change the amount, meal or count by editing the request.
type SelfServicePreview struct {
	Status        string `json:"status"` // READY or UNAVAILABLE
	Code          string `json:"code,omitempty"`
	Message       string `json:"message,omitempty"`
	MealCode      string `json:"meal_code,omitempty"`
	MealName      string `json:"meal_name,omitempty"`
	BusinessDate  string `json:"business_date,omitempty"`
	AmountCents   int64  `json:"amount_cents,omitempty"`
	ExistingCount int    `json:"existing_count"`
	IntentID      string `json:"intent_id,omitempty"`
}

// SelfServiceOutcome is the result of confirming or re-reading one intent.
// SUCCESS carries the same consumption details the scan success page shows.
// CONFIRM_REQUIRED means the preview changed and the employee must look again.
type SelfServiceOutcome struct {
	Status      string              `json:"status"` // SUCCESS, REJECTED or CONFIRM_REQUIRED
	Code        string              `json:"code,omitempty"`
	Message     string              `json:"message,omitempty"`
	Consumption *Result             `json:"consumption,omitempty"`
	Preview     *SelfServicePreview `json:"preview,omitempty"`
}

type selfServiceIntent struct {
	ID           string
	EmployeeID   int64
	SessionID    int64
	State        string
	MealCode     string
	MealName     string
	BusinessDate string
	Amount       int64
	Existing     int
	ResultCode   string
	Transaction  int64
}

const intentColumns = `id,employee_id,session_id,state,meal_code,meal_name,business_date,
	amount_cents,existing_count,COALESCE(result_code,''),COALESCE(transaction_id,0)`

func scanIntent(row *sql.Row) (selfServiceIntent, error) {
	var i selfServiceIntent
	err := row.Scan(&i.ID, &i.EmployeeID, &i.SessionID, &i.State, &i.MealCode, &i.MealName,
		&i.BusinessDate, &i.Amount, &i.Existing, &i.ResultCode, &i.Transaction)
	return i, err
}

func (s *Service) intentByID(ctx context.Context, tx *sql.Tx, id string) (selfServiceIntent, error) {
	return scanIntent(tx.QueryRowContext(ctx, `SELECT `+intentColumns+` FROM self_service_intents WHERE id=?`, id))
}

func (s *Service) liveIntentForSession(ctx context.Context, tx *sql.Tx, sessionID int64) (selfServiceIntent, error) {
	return scanIntent(tx.QueryRowContext(ctx, `SELECT `+intentColumns+` FROM self_service_intents
		WHERE session_id=? AND state='PENDING' ORDER BY created_at DESC, rowid DESC LIMIT 1`, sessionID))
}

// consumptionCount is the single rule for "how many times has this employee
// already consumed in this business date and meal". It is shared by the scan
// repeat-confirmation decision and the self-service preview so both entrances
// agree. It counts self-service, terminal and manual-supply consumptions that
// were not fully refunded; PENDING requests are not consumptions.
func consumptionCount(ctx context.Context, tx *sql.Tx, accountID int64, date, code string, out *int) error {
	return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transactions t WHERE t.account_id = ? AND t.type = 'CONSUME'
		AND (t.business_id = ? OR (t.business_type = 'MANUAL_SUPPLY' AND EXISTS
			(SELECT 1 FROM manual_supplies m WHERE m.receipt_ref = t.business_id AND m.business_date = ? AND m.meal_code = ?)))
		AND NOT EXISTS (SELECT 1 FROM transactions refund WHERE refund.type = 'REFUND' AND refund.related_transaction_id = t.id)`,
		accountID, date+"|"+code, date, code).Scan(out)
}

// SelfServiceSession is the explicit server operation that runs when the
// employee opens the fixed link. In one transaction it cancels every scan
// request this employee still has waiting for confirmation, then returns the
// current preview. Retrying is idempotent: the same pending intent is reused
// and no funds move. Nothing here charges the employee.
func (s *Service) SelfServiceSession(ctx context.Context, principal employees.Principal) (SelfServicePreview, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SelfServicePreview{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if err := s.expirePending(ctx, tx, now); err != nil {
		return SelfServicePreview{}, err
	}
	// Auto-cancel is scoped to this employee only: other employees' requests and
	// already processed consumptions are untouched.
	if err := s.cancelOwnPendingScans(ctx, tx, principal.ID); err != nil {
		return SelfServicePreview{}, err
	}
	preview, err := s.currentPreview(ctx, tx, principal, now)
	if err != nil {
		return SelfServicePreview{}, err
	}
	if err := tx.Commit(); err != nil {
		return SelfServicePreview{}, err
	}
	return preview, nil
}

// cancelOwnPendingScans finishes this employee's waiting scan requests with the
// same CANCELLED terminal state a manual employee cancel produces, so the
// terminal stops waiting and can observe the final state by id.
func (s *Service) cancelOwnPendingScans(ctx context.Context, tx *sql.Tx, employeeID int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT `+pendingColumns+` FROM pending_consumptions p
		JOIN payment_tokens t ON t.id=p.token_id JOIN accounts a ON a.employee_id=p.employee_id
		WHERE p.employee_id=? AND p.state='PENDING' ORDER BY p.rowid`, employeeID)
	if err != nil {
		return err
	}
	records := make([]pendingRecord, 0)
	for rows.Next() {
		p, err := scanPendingRows(rows)
		if err != nil {
			rows.Close()
			return err
		}
		records = append(records, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, pending := range records {
		if err := s.finalizePending(ctx, tx, pending, "CANCELLED"); err != nil {
			return err
		}
	}
	return nil
}

// currentPreview resolves the server's decision and the intent that carries it.
// It returns Status UNAVAILABLE (and no intent) when the employee cannot consume
// right now, so the page can explain why without offering a confirm action.
func (s *Service) currentPreview(ctx context.Context, tx *sql.Tx, principal employees.Principal, now time.Time) (SelfServicePreview, error) {
	var accountID int64
	var employeeStatus, accountStatus string
	var mustChange int
	err := tx.QueryRowContext(ctx, `SELECT a.id,e.status,a.status,e.must_change_password
		FROM employees e JOIN accounts a ON a.employee_id=e.id WHERE e.id=?`, principal.ID).
		Scan(&accountID, &employeeStatus, &accountStatus, &mustChange)
	if errors.Is(err, sql.ErrNoRows) {
		return SelfServicePreview{}, ErrNotFound
	}
	if err != nil {
		return SelfServicePreview{}, err
	}
	if employeeStatus != "ACTIVE" || accountStatus != "ACTIVE" || mustChange != 0 || !principal.CanIssuePaymentToken() {
		return SelfServicePreview{Status: "UNAVAILABLE", Code: "ACCOUNT_UNAVAILABLE", Message: "账户不可用，无法自助消费"}, nil
	}
	local := now.In(s.location)
	date := local.Format("2006-01-02")
	period, err := s.meals.ActiveAtTx(ctx, tx, now)
	if errors.Is(err, meals.ErrNoActivePeriod) {
		return SelfServicePreview{Status: "UNAVAILABLE", Code: "NO_ACTIVE_PERIOD", Message: "当前不在用餐时段"}, nil
	}
	if err != nil {
		return SelfServicePreview{}, err
	}
	endAt, err := mealEnd(local, period.EndTime)
	if err != nil {
		return SelfServicePreview{}, err
	}
	if !now.Before(endAt) {
		return SelfServicePreview{Status: "UNAVAILABLE", Code: "NO_ACTIVE_PERIOD", Message: "当前不在用餐时段"}, nil
	}
	var existing int
	if err := consumptionCount(ctx, tx, accountID, date, period.Code, &existing); err != nil {
		return SelfServicePreview{}, err
	}
	intent, err := s.reusableIntent(ctx, tx, principal, period, date, existing, endAt)
	if err != nil {
		return SelfServicePreview{}, err
	}
	return SelfServicePreview{Status: "READY", MealCode: period.Code, MealName: period.Name,
		BusinessDate: date, AmountCents: period.PriceCents, ExistingCount: existing, IntentID: intent.ID}, nil
}

// reusableIntent keeps a retried session operation idempotent: while an
// unconsumed intent still matches the current meal, date, price and count, it is
// returned instead of creating another one.
func (s *Service) reusableIntent(ctx context.Context, tx *sql.Tx, principal employees.Principal, period meals.Period, date string, existing int, endAt time.Time) (selfServiceIntent, error) {
	live, err := s.liveIntentForSession(ctx, tx, principal.SessionID)
	if err == nil && live.MealCode == period.Code && live.BusinessDate == date &&
		live.Amount == period.PriceCents && live.Existing == existing {
		return live, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return selfServiceIntent{}, err
	}
	if err == nil {
		// The preview changed under the employee's feet; the old intent must not
		// be usable with a stale amount, meal or count.
		if _, err := tx.ExecContext(ctx, `UPDATE self_service_intents SET state='SUPERSEDED', updated_at=?
			WHERE session_id=? AND state='PENDING'`, time.Now().UTC().Format(time.RFC3339Nano), principal.SessionID); err != nil {
			return selfServiceIntent{}, err
		}
	}
	id, err := randomID("ssi_")
	if err != nil {
		return selfServiceIntent{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO self_service_intents
		(id,employee_id,session_id,state,meal_code,meal_name,business_date,amount_cents,existing_count,meal_end_at,created_at,updated_at)
		VALUES (?,?,?,'PENDING',?,?,?,?,?,?,?,?)`, id, principal.ID, principal.SessionID,
		period.Code, period.Name, date, period.PriceCents, existing, endAt.Unix(), now, now); err != nil {
		return selfServiceIntent{}, err
	}
	return selfServiceIntent{ID: id, EmployeeID: principal.ID, SessionID: principal.SessionID, State: "PENDING",
		MealCode: period.Code, MealName: period.Name, BusinessDate: date, Amount: period.PriceCents, Existing: existing}, nil
}

// ConfirmSelfService charges the employee's stored-value account after an
// explicit confirmation. Inside one transaction it re-checks the live session,
// employee and account status, balance, current meal and business date, price
// and existing count. The intent id is the stable idempotency identifier, so
// concurrent submits, retries and result reads all observe at most one
// consumption and one ledger entry.
func (s *Service) ConfirmSelfService(ctx context.Context, principal employees.Principal, intentID string) (SelfServiceOutcome, error) {
	if len(intentID) != 36 || intentID[:4] != "ssi_" {
		return SelfServiceOutcome{}, ErrInvalidRequest
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SelfServiceOutcome{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	intent, err := s.intentByID(ctx, tx, intentID)
	if errors.Is(err, sql.ErrNoRows) {
		return SelfServiceOutcome{}, ErrNotFound
	}
	if err != nil {
		return SelfServiceOutcome{}, err
	}
	if intent.EmployeeID != principal.ID || intent.SessionID != principal.SessionID {
		return SelfServiceOutcome{}, ErrNotFound
	}
	switch intent.State {
	case "CONSUMED":
		result, err := s.processedResult(ctx, tx, intent.Transaction)
		if err != nil {
			return SelfServiceOutcome{}, err
		}
		result.Replayed = true
		return SelfServiceOutcome{Status: "SUCCESS", Code: result.Code, Message: result.Message, Consumption: &result}, tx.Commit()
	case "FAILED":
		return SelfServiceOutcome{Status: "REJECTED", Code: intent.ResultCode,
			Message: selfServiceMessage(intent.ResultCode)}, tx.Commit()
	case "SUPERSEDED":
		preview, err := s.currentPreview(ctx, tx, principal, now)
		if err != nil {
			return SelfServiceOutcome{}, err
		}
		return SelfServiceOutcome{Status: "CONFIRM_REQUIRED", Code: "PREVIEW_CHANGED",
			Message: "餐次信息已更新，请确认后再消费", Preview: &preview}, tx.Commit()
	}
	if err := s.checkSessionLive(ctx, tx, intent); err != nil {
		if errors.Is(err, ledger.ErrAccountUnavailable) {
			if err := s.failIntent(ctx, tx, intent, "ACCOUNT_UNAVAILABLE"); err != nil {
				return SelfServiceOutcome{}, err
			}
			return SelfServiceOutcome{Status: "REJECTED", Code: "ACCOUNT_UNAVAILABLE",
				Message: "账户不可用"}, tx.Commit()
		}
		return SelfServiceOutcome{}, err
	}
	// Meal ended or no current meal: refuse. The intent is finished so a retry
	// does not charge either.
	preview, err := s.currentPreview(ctx, tx, principal, now)
	if err != nil {
		return SelfServiceOutcome{}, err
	}
	if preview.Status != "READY" {
		if err := s.failIntent(ctx, tx, intent, preview.Code); err != nil {
			return SelfServiceOutcome{}, err
		}
		return SelfServiceOutcome{Status: "REJECTED", Code: preview.Code, Message: preview.Message}, tx.Commit()
	}
	// Anything the employee saw differently means the old confirmation no longer
	// covers this charge: return the fresh preview and require a new confirm.
	if preview.MealCode != intent.MealCode || preview.BusinessDate != intent.BusinessDate ||
		preview.AmountCents != intent.Amount || preview.ExistingCount != intent.Existing ||
		preview.IntentID != intent.ID {
		if err := s.failIntent(ctx, tx, intent, "PREVIEW_CHANGED"); err != nil {
			return SelfServiceOutcome{}, err
		}
		return SelfServiceOutcome{Status: "CONFIRM_REQUIRED", Code: "PREVIEW_CHANGED",
			Message: "餐次或次数已更新，请确认后再消费", Preview: &preview}, tx.Commit()
	}
	result, err := s.applySelfServiceConsumption(ctx, tx, intent, principal)
	if errors.Is(err, ledger.ErrInsufficientFunds) {
		if err := s.failIntent(ctx, tx, intent, "INSUFFICIENT_FUNDS"); err != nil {
			return SelfServiceOutcome{}, err
		}
		return SelfServiceOutcome{Status: "REJECTED", Code: "INSUFFICIENT_FUNDS", Message: "余额不足"}, tx.Commit()
	}
	if errors.Is(err, ledger.ErrAccountUnavailable) {
		if err := s.failIntent(ctx, tx, intent, "ACCOUNT_UNAVAILABLE"); err != nil {
			return SelfServiceOutcome{}, err
		}
		return SelfServiceOutcome{Status: "REJECTED", Code: "ACCOUNT_UNAVAILABLE", Message: "账户不可用"}, tx.Commit()
	}
	if err != nil {
		return SelfServiceOutcome{}, err
	}
	return SelfServiceOutcome{Status: "SUCCESS", Code: result.Code, Message: result.Message, Consumption: &result}, tx.Commit()
}

// SelfServiceStatus re-reads one intent without cancelling anything, so a
// refreshed success page recovers the same consumption instead of guessing from
// the balance.
func (s *Service) SelfServiceStatus(ctx context.Context, principal employees.Principal, intentID string) (SelfServiceOutcome, error) {
	if len(intentID) != 36 || intentID[:4] != "ssi_" {
		return SelfServiceOutcome{}, ErrInvalidRequest
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SelfServiceOutcome{}, err
	}
	defer tx.Rollback()
	intent, err := s.intentByID(ctx, tx, intentID)
	if errors.Is(err, sql.ErrNoRows) {
		return SelfServiceOutcome{}, ErrNotFound
	}
	if err != nil {
		return SelfServiceOutcome{}, err
	}
	if intent.EmployeeID != principal.ID || intent.SessionID != principal.SessionID {
		return SelfServiceOutcome{}, ErrNotFound
	}
	switch intent.State {
	case "CONSUMED":
		result, err := s.processedResult(ctx, tx, intent.Transaction)
		if err != nil {
			return SelfServiceOutcome{}, err
		}
		result.Replayed = true
		return SelfServiceOutcome{Status: "SUCCESS", Code: result.Code, Message: result.Message, Consumption: &result}, tx.Commit()
	case "FAILED":
		return SelfServiceOutcome{Status: "REJECTED", Code: intent.ResultCode,
			Message: selfServiceMessage(intent.ResultCode)}, tx.Commit()
	}
	preview, err := s.currentPreview(ctx, tx, principal, time.Now().UTC())
	if err != nil {
		return SelfServiceOutcome{}, err
	}
	return SelfServiceOutcome{Status: "CONFIRM_REQUIRED", Code: "AWAITING_CONFIRM",
		Message: "等待确认", Preview: &preview}, tx.Commit()
}

// applySelfServiceConsumption writes the CONSUME transaction with its own
// business source, so administrators can tell self-service apart from terminal
// scans and manual supply, and records the consumption snapshot. The ledger id
// keyed on the intent makes a repeated confirm a replay rather than a new charge.
func (s *Service) applySelfServiceConsumption(ctx context.Context, tx *sql.Tx, intent selfServiceIntent, principal employees.Principal) (Result, error) {
	var accountID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE employee_id=?`, principal.ID).Scan(&accountID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Result{}, ledger.ErrAccountNotFound
		}
		return Result{}, err
	}
	fund, err := ledger.ApplyInTx(ctx, tx, ledger.Request{AccountID: accountID, Kind: ledger.Consume,
		Amount: -intent.Amount, BusinessType: "SELF_SERVICE", BusinessID: intent.BusinessDate + "|" + intent.MealCode,
		IdempotencyKey: "selfservice:" + intent.ID})
	if err != nil {
		return Result{}, err
	}
	if !fund.Replayed {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO consumption_details(transaction_id,meal_code,meal_name,business_date,amount_cents)
			VALUES (?,?,?,?,?)`, fund.Entry.ID, intent.MealCode, intent.MealName, intent.BusinessDate, intent.Amount); err != nil {
			return Result{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE self_service_intents SET state='CONSUMED',transaction_id=?,updated_at=?
		WHERE id=? AND state='PENDING'`, fund.Entry.ID, time.Now().UTC().Format(time.RFC3339Nano), intent.ID); err != nil {
		return Result{}, err
	}
	var name string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM employees WHERE id=?`, principal.ID).Scan(&name); err != nil {
		return Result{}, err
	}
	if err := store.RecordAudit(ctx, tx, 0, "SELF_SERVICE_CONSUMED", "transaction",
		fmt.Sprintf("%d", fund.Entry.ID), map[string]any{"employee_id": principal.ID, "intent_id": intent.ID,
			"meal_code": intent.MealCode, "business_date": intent.BusinessDate, "amount_cents": intent.Amount}); err != nil {
		return Result{}, err
	}
	return Result{Status: "SUCCESS", Code: "CONSUMED", Message: "消费成功", EmployeeName: name,
		MealCode: intent.MealCode, MealName: intent.MealName, AmountCents: intent.Amount,
		TransactionID: fund.Entry.ID, TransactionNo: fund.Entry.TransactionNo,
		ConsumptionNo: fmt.Sprintf("C%010d", fund.Entry.ID),
		OccurredAt:    fund.Entry.CreatedAt.Format(time.RFC3339Nano)}, nil
}

func (s *Service) failIntent(ctx context.Context, tx *sql.Tx, intent selfServiceIntent, code string) error {
	_, err := tx.ExecContext(ctx, `UPDATE self_service_intents SET state='FAILED',result_code=?,updated_at=?
		WHERE id=? AND state='PENDING'`, code, time.Now().UTC().Format(time.RFC3339Nano), intent.ID)
	return err
}

// checkSessionLive rejects a confirmation whose login session or account is no
// longer usable, without charging anything.
func (s *Service) checkSessionLive(ctx context.Context, tx *sql.Tx, intent selfServiceIntent) error {
	var employeeStatus, accountStatus string
	var mustChange, sessionExpiry int64
	var revokedAt sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT e.status,a.status,e.must_change_password,s.expires_at,s.revoked_at
		FROM employee_sessions s JOIN employees e ON e.id=s.employee_id
		JOIN accounts a ON a.employee_id=e.id WHERE s.id=? AND s.employee_id=?`,
		intent.SessionID, intent.EmployeeID).Scan(&employeeStatus, &accountStatus, &mustChange, &sessionExpiry, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrEmployeeSessionInvalid
	}
	if err != nil {
		return err
	}
	if revokedAt.Valid || sessionExpiry <= time.Now().UTC().Unix() {
		return ErrEmployeeSessionInvalid
	}
	if employeeStatus != "ACTIVE" || accountStatus != "ACTIVE" || mustChange != 0 {
		return ledger.ErrAccountUnavailable
	}
	return nil
}

func selfServiceMessage(code string) string {
	switch code {
	case "INSUFFICIENT_FUNDS":
		return "余额不足"
	case "ACCOUNT_UNAVAILABLE":
		return "账户不可用"
	case "NO_ACTIVE_PERIOD":
		return "当前不在用餐时段"
	case "PREVIEW_CHANGED":
		return "餐次信息已更新，请确认后再消费"
	default:
		return "消费未完成"
	}
}
