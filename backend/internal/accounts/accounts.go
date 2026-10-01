package accounts

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/employees"
	"github.com/EziosWJ/canteen-wallet/backend/internal/ledger"
	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
)

var (
	ErrInvalidInput   = errors.New("invalid account operation")
	ErrNotFound       = errors.New("account not found")
	ErrInvalidState   = errors.New("account state does not allow operation")
	ErrPayoutConflict = errors.New("payout receipt already used or retry details differ")
	ErrDataIntegrity  = errors.New("payout business record is missing")
)

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

type AdjustInput struct {
	AmountCents    int64  `json:"amount_cents"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
}

type WithdrawInput struct {
	PayoutRef                  string    `json:"payout_ref"`
	PaidAt                     time.Time `json:"paid_at"`
	PaymentMethod              string    `json:"payment_method"`
	IdempotencyKey             string    `json:"idempotency_key"`
	RelatedRefundTransactionID int64     `json:"related_refund_transaction_id"`
}

type PayoutReceipt struct {
	ID                      int64     `json:"id"`
	PayoutRef               string    `json:"payout_ref"`
	EmployeeID              int64     `json:"employee_id"`
	AmountCents             int64     `json:"amount_cents"`
	PaidAt                  time.Time `json:"paid_at"`
	PaymentMethod           string    `json:"payment_method"`
	RecordedBy              int64     `json:"recorded_by"`
	WithdrawalTransactionID int64     `json:"withdrawal_transaction_id"`
}

type WithdrawResult struct {
	Transaction ledger.Entry  `json:"-"`
	Receipt     PayoutReceipt `json:"receipt"`
	Replayed    bool          `json:"replayed"`
}

func (s *Service) Adjust(ctx context.Context, actorID, accountID int64, input AdjustInput) (ledger.Result, error) {
	input.Reason = strings.TrimSpace(input.Reason)
	if actorID < 1 || accountID < 1 || input.AmountCents == 0 || input.AmountCents == math.MinInt64 ||
		!validText(input.Reason, 512) || !validText(input.IdempotencyKey, 128) {
		return ledger.Result{}, ErrInvalidInput
	}
	return ledger.New(s.db).Apply(ctx, ledger.Request{
		AccountID: accountID, Kind: ledger.BalanceAdjustment, Amount: input.AmountCents,
		AdministratorID: actorID, BusinessType: "ADJUSTMENT_CASE", BusinessID: input.IdempotencyKey,
		IdempotencyKey: input.IdempotencyKey, Reason: input.Reason,
	})
}

// Withdraw records an offline payment before closing an open account. After
// closure, only an explicitly linked historical refund can be paid out.
func (s *Service) Withdraw(ctx context.Context, actorID, accountID int64, input WithdrawInput) (WithdrawResult, error) {
	input.PayoutRef = strings.TrimSpace(input.PayoutRef)
	if actorID < 1 || accountID < 1 || !validText(input.PayoutRef, 128) ||
		!validText(input.IdempotencyKey, 128) || input.PaidAt.IsZero() || input.RelatedRefundTransactionID < 0 ||
		(input.PaymentMethod != "CASH" && input.PaymentMethod != "BANK_TRANSFER" && input.PaymentMethod != "OTHER") {
		return WithdrawResult{}, ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WithdrawResult{}, err
	}
	defer tx.Rollback()

	// The original amount is no longer present in the account balance on retry.
	previous, err := transactionByKey(ctx, tx, input.IdempotencyKey)
	if err == nil {
		if previous.Kind != ledger.BalanceWithdrawal || previous.AccountID != accountID ||
			previous.AdministratorID != actorID || previous.BusinessID != input.PayoutRef ||
			previous.RelatedTransactionID != input.RelatedRefundTransactionID {
			return WithdrawResult{}, ledger.ErrIdempotencyConflict
		}
		receipt, err := receiptForTransaction(ctx, tx, previous.ID)
		if err != nil {
			return WithdrawResult{}, err
		}
		if !receipt.PaidAt.Equal(input.PaidAt) ||
			receipt.PaymentMethod != input.PaymentMethod || receipt.RecordedBy != actorID ||
			receipt.PayoutRef != input.PayoutRef || receipt.AmountCents != -previous.Amount {
			return WithdrawResult{}, ErrPayoutConflict
		}
		return WithdrawResult{Transaction: previous, Receipt: receipt, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WithdrawResult{}, err
	}

	var employeeID, balance int64
	var accountStatus, employeeStatus string
	err = tx.QueryRowContext(ctx, `SELECT a.employee_id, a.balance, a.status, e.status
		FROM accounts a JOIN employees e ON e.id = a.employee_id WHERE a.id = ?`, accountID).
		Scan(&employeeID, &balance, &accountStatus, &employeeStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return WithdrawResult{}, ErrNotFound
	}
	if err != nil {
		return WithdrawResult{}, err
	}
	if accountStatus != employeeStatus {
		return WithdrawResult{}, ErrInvalidState
	}

	var amount int64
	switch accountStatus {
	case "ACTIVE", "FROZEN":
		if input.RelatedRefundTransactionID != 0 || balance == 0 {
			return WithdrawResult{}, ErrInvalidState
		}
		amount = balance
	case "CLOSED":
		if input.RelatedRefundTransactionID == 0 {
			return WithdrawResult{}, ErrInvalidState
		}
		var refundAmount int64
		err = tx.QueryRowContext(ctx, `SELECT amount FROM transactions WHERE id = ? AND account_id = ? AND type = 'REFUND'`,
			input.RelatedRefundTransactionID, accountID).Scan(&refundAmount)
		if errors.Is(err, sql.ErrNoRows) {
			return WithdrawResult{}, ledger.ErrInvalidReference
		}
		if err != nil {
			return WithdrawResult{}, err
		}
		if refundAmount < 1 {
			return WithdrawResult{}, ErrDataIntegrity
		}
		// A refund posted before the full-balance account-closing payout was
		// already returned through the wallet balance. Do not offer it again as
		// a separate cash payout after closure.
		var closingPayoutID int64
		err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT w.id FROM transactions w
			JOIN payout_receipts p ON p.withdrawal_transaction_id=w.id
			WHERE w.account_id=? AND p.employee_id=? AND w.type='BALANCE_WITHDRAWAL'
			AND w.related_transaction_id IS NULL AND w.id>?
			ORDER BY w.id LIMIT 1),0)`, accountID, employeeID, input.RelatedRefundTransactionID).Scan(&closingPayoutID)
		if err != nil {
			return WithdrawResult{}, err
		}
		if closingPayoutID > 0 {
			return WithdrawResult{}, ledger.ErrDuplicateReference
		}
		amount = refundAmount
	default:
		return WithdrawResult{}, ErrInvalidState
	}
	if balance < amount {
		return WithdrawResult{}, ledger.ErrInsufficientFunds
	}
	posted, err := ledger.ApplyInTx(ctx, tx, ledger.Request{
		AccountID: accountID, Kind: ledger.BalanceWithdrawal, Amount: -amount,
		AdministratorID: actorID, BusinessType: "PAYOUT_RECEIPT", BusinessID: input.PayoutRef,
		RelatedTransactionID: input.RelatedRefundTransactionID, IdempotencyKey: input.IdempotencyKey,
	})
	if errors.Is(err, ledger.ErrBusinessConflict) {
		return WithdrawResult{}, ErrPayoutConflict
	}
	if err != nil {
		return WithdrawResult{}, err
	}
	if posted.Replayed {
		return WithdrawResult{}, ErrPayoutConflict
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `INSERT INTO payout_receipts
		(payout_ref, employee_id, amount_cents, paid_at, payment_method, recorded_by, withdrawal_transaction_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, input.PayoutRef, employeeID, amount,
		input.PaidAt.UTC().Format(time.RFC3339Nano), input.PaymentMethod, actorID, posted.Entry.ID, now)
	if err != nil {
		return WithdrawResult{}, err
	}
	receiptID, err := result.LastInsertId()
	if err != nil {
		return WithdrawResult{}, err
	}
	if accountStatus != "CLOSED" {
		if _, err := tx.ExecContext(ctx, `UPDATE employees SET status = 'CLOSED', updated_at = ? WHERE id = ?`, now, employeeID); err != nil {
			return WithdrawResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE accounts SET status = 'CLOSED', updated_at = ? WHERE id = ?`, now, accountID); err != nil {
			return WithdrawResult{}, err
		}
		if _, err := employees.RevokeSessions(ctx, tx, employeeID); err != nil {
			return WithdrawResult{}, err
		}
	}
	if err := store.RecordAudit(ctx, tx, actorID, "BALANCE_WITHDRAWN", "payout_receipt",
		strconv.FormatInt(receiptID, 10), map[string]any{
			"transaction_id": posted.Entry.ID, "employee_id": employeeID,
			"payout_ref": input.PayoutRef, "amount_cents": amount,
			"related_refund_transaction_id": input.RelatedRefundTransactionID,
			"account_closed":                accountStatus != "CLOSED",
		}); err != nil {
		return WithdrawResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return WithdrawResult{}, err
	}
	return WithdrawResult{Transaction: posted.Entry, Receipt: PayoutReceipt{
		ID: receiptID, PayoutRef: input.PayoutRef, EmployeeID: employeeID,
		AmountCents: amount, PaidAt: input.PaidAt.UTC(), PaymentMethod: input.PaymentMethod,
		RecordedBy: actorID, WithdrawalTransactionID: posted.Entry.ID,
	}}, nil
}

func transactionByKey(ctx context.Context, tx *sql.Tx, key string) (ledger.Entry, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM transactions WHERE idempotency_key = ?`, key).Scan(&id)
	if err != nil {
		return ledger.Entry{}, err
	}
	// Ledger.Get uses the same connection pool; avoid waiting for the active tx.
	return scanTransaction(ctx, tx, id)
}

func scanTransaction(ctx context.Context, tx *sql.Tx, id int64) (ledger.Entry, error) {
	var entry ledger.Entry
	var adminID, relatedID sql.NullInt64
	var terminalID, reason sql.NullString
	var createdAt string
	err := tx.QueryRowContext(ctx, `SELECT id, transaction_no, account_id, type, amount, before_balance,
		after_balance, administrator_id, terminal_id, business_type, business_id,
		related_transaction_id, idempotency_key, reason, created_at FROM transactions WHERE id = ?`, id).
		Scan(&entry.ID, &entry.TransactionNo, &entry.AccountID, &entry.Kind, &entry.Amount,
			&entry.BeforeBalance, &entry.AfterBalance, &adminID, &terminalID, &entry.BusinessType,
			&entry.BusinessID, &relatedID, &entry.IdempotencyKey, &reason, &createdAt)
	if err != nil {
		return ledger.Entry{}, err
	}
	entry.AdministratorID, entry.RelatedTransactionID = adminID.Int64, relatedID.Int64
	entry.TerminalID, entry.Reason = terminalID.String, reason.String
	entry.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	return entry, err
}

func receiptForTransaction(ctx context.Context, tx *sql.Tx, id int64) (PayoutReceipt, error) {
	var receipt PayoutReceipt
	var paidAt string
	err := tx.QueryRowContext(ctx, `SELECT id, payout_ref, employee_id, amount_cents, paid_at,
		payment_method, recorded_by, withdrawal_transaction_id FROM payout_receipts
		WHERE withdrawal_transaction_id = ?`, id).Scan(&receipt.ID, &receipt.PayoutRef,
		&receipt.EmployeeID, &receipt.AmountCents, &paidAt, &receipt.PaymentMethod,
		&receipt.RecordedBy, &receipt.WithdrawalTransactionID)
	if errors.Is(err, sql.ErrNoRows) {
		return PayoutReceipt{}, ErrDataIntegrity
	}
	if err != nil {
		return PayoutReceipt{}, err
	}
	receipt.PaidAt, err = time.Parse(time.RFC3339Nano, paidAt)
	if err != nil {
		return PayoutReceipt{}, ErrDataIntegrity
	}
	return receipt, nil
}

func validText(value string, max int) bool {
	return len(value) <= max && strings.TrimSpace(value) != ""
}
