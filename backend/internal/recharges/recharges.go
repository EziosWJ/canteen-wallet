package recharges

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/ledger"
	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
)

var (
	ErrInvalidInput     = errors.New("invalid recharge request")
	ErrEmployeeNotFound = errors.New("employee not found")
	ErrReceiptConflict  = errors.New("receipt already used or retry details differ")
	ErrRechargeNotFound = errors.New("recharge not found")
	ErrAlreadyReversed  = errors.New("recharge already reversed")
	ErrDataIntegrity    = errors.New("recharge business record is missing")
)

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

type ConfirmInput struct {
	EmployeeID     int64     `json:"employee_id"`
	AmountCents    int64     `json:"amount_cents"`
	ReceiptRef     string    `json:"receipt_ref"`
	CollectedAt    time.Time `json:"collected_at"`
	PaymentMethod  string    `json:"payment_method"`
	IdempotencyKey string    `json:"idempotency_key"`
}

type ReverseInput struct {
	IdempotencyKey string `json:"idempotency_key"`
	Reason         string `json:"reason"`
}

type Receipt struct {
	ID                    int64     `json:"id"`
	ReceiptRef            string    `json:"receipt_ref"`
	EmployeeID            int64     `json:"employee_id"`
	AmountCents           int64     `json:"amount_cents"`
	CollectedAt           time.Time `json:"collected_at"`
	PaymentMethod         string    `json:"payment_method"`
	RecordedBy            int64     `json:"recorded_by"`
	RechargeTransactionID int64     `json:"recharge_transaction_id"`
}

type ConfirmResult struct {
	Transaction ledger.Entry
	Receipt     Receipt
	Replayed    bool
}

type ReverseResult struct {
	Transaction           ledger.Entry
	RechargeTransactionID int64
	Replayed              bool
}

// Confirm records a verified offline receipt and its funds movement atomically.
// A receipt reference has one owner; a changed idempotency key never replays it.
func (s *Service) Confirm(ctx context.Context, actorID int64, input ConfirmInput) (ConfirmResult, error) {
	input.ReceiptRef = strings.TrimSpace(input.ReceiptRef)
	if actorID < 1 || input.EmployeeID < 1 || input.AmountCents < 1 || input.CollectedAt.IsZero() ||
		!validText(input.ReceiptRef, 128) || !validText(input.IdempotencyKey, 128) ||
		(input.PaymentMethod != "CASH" && input.PaymentMethod != "BANK_TRANSFER" && input.PaymentMethod != "OTHER") {
		return ConfirmResult{}, ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ConfirmResult{}, err
	}
	defer tx.Rollback()
	var accountID int64
	err = tx.QueryRowContext(ctx, `SELECT a.id FROM employees e JOIN accounts a ON a.employee_id = e.id WHERE e.id = ?`,
		input.EmployeeID).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return ConfirmResult{}, ErrEmployeeNotFound
	}
	if err != nil {
		return ConfirmResult{}, err
	}
	posted, err := ledger.ApplyInTx(ctx, tx, ledger.Request{
		AccountID: accountID, Kind: ledger.Recharge, Amount: input.AmountCents,
		AdministratorID: actorID, BusinessType: "RECEIPT", BusinessID: input.ReceiptRef,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return ConfirmResult{}, err
	}
	if posted.Replayed {
		if posted.Entry.IdempotencyKey != input.IdempotencyKey {
			return ConfirmResult{}, ErrReceiptConflict
		}
		receipt, err := findReceipt(ctx, tx, posted.Entry.ID)
		if err != nil {
			return ConfirmResult{}, err
		}
		if receipt.ReceiptRef != input.ReceiptRef || receipt.EmployeeID != input.EmployeeID ||
			receipt.AmountCents != input.AmountCents || !receipt.CollectedAt.Equal(input.CollectedAt) ||
			receipt.PaymentMethod != input.PaymentMethod || receipt.RecordedBy != actorID {
			return ConfirmResult{}, ErrReceiptConflict
		}
		return ConfirmResult{Transaction: posted.Entry, Receipt: receipt, Replayed: true}, nil
	}
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `INSERT INTO recharge_receipts
		(receipt_ref, employee_id, amount_cents, collected_at, payment_method, recorded_by, recharge_transaction_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, input.ReceiptRef, input.EmployeeID, input.AmountCents,
		input.CollectedAt.UTC().Format(time.RFC3339Nano), input.PaymentMethod, actorID, posted.Entry.ID, createdAt)
	if err != nil {
		return ConfirmResult{}, err
	}
	receiptID, err := result.LastInsertId()
	if err != nil {
		return ConfirmResult{}, err
	}
	if err := store.RecordAudit(ctx, tx, actorID, "RECHARGE_CONFIRMED", "recharge_receipt",
		strconv.FormatInt(receiptID, 10), map[string]any{
			"transaction_id": posted.Entry.ID, "employee_id": input.EmployeeID,
			"receipt_ref": input.ReceiptRef, "amount_cents": input.AmountCents,
		}); err != nil {
		return ConfirmResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ConfirmResult{}, err
	}
	return ConfirmResult{Transaction: posted.Entry, Receipt: Receipt{
		ID: receiptID, ReceiptRef: input.ReceiptRef, EmployeeID: input.EmployeeID,
		AmountCents: input.AmountCents, CollectedAt: input.CollectedAt.UTC(),
		PaymentMethod: input.PaymentMethod, RecordedBy: actorID, RechargeTransactionID: posted.Entry.ID,
	}}, nil
}

// Reverse posts exactly the negative amount of an existing recharge. The
// original transaction and receipt remain immutable and available for audit.
func (s *Service) Reverse(ctx context.Context, actorID, originalID int64, input ReverseInput) (ReverseResult, error) {
	if actorID < 1 || originalID < 1 || !validText(input.IdempotencyKey, 128) || !validText(input.Reason, 512) {
		return ReverseResult{}, ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReverseResult{}, err
	}
	defer tx.Rollback()
	var accountID, amount int64
	err = tx.QueryRowContext(ctx, `SELECT account_id, amount FROM transactions WHERE id = ? AND type = 'RECHARGE'`, originalID).
		Scan(&accountID, &amount)
	if errors.Is(err, sql.ErrNoRows) {
		return ReverseResult{}, ErrRechargeNotFound
	}
	if err != nil {
		return ReverseResult{}, err
	}
	if amount < 1 || amount == math.MinInt64 {
		return ReverseResult{}, ErrDataIntegrity
	}
	var receiptID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM recharge_receipts WHERE recharge_transaction_id = ?`, originalID).Scan(&receiptID)
	if errors.Is(err, sql.ErrNoRows) {
		return ReverseResult{}, ErrDataIntegrity
	}
	if err != nil {
		return ReverseResult{}, err
	}
	var existingKey string
	err = tx.QueryRowContext(ctx, `SELECT t.idempotency_key FROM recharge_reversals rr
		JOIN transactions t ON t.id = rr.reversal_transaction_id
		WHERE rr.recharge_transaction_id = ?`, originalID).Scan(&existingKey)
	if err == nil && existingKey != input.IdempotencyKey {
		return ReverseResult{}, ErrAlreadyReversed
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ReverseResult{}, err
	}
	posted, err := ledger.ApplyInTx(ctx, tx, ledger.Request{
		AccountID: accountID, Kind: ledger.RechargeReversal, Amount: -amount,
		AdministratorID: actorID, BusinessType: "RECHARGE", BusinessID: strconv.FormatInt(originalID, 10),
		RelatedTransactionID: originalID, IdempotencyKey: input.IdempotencyKey, Reason: input.Reason,
	})
	if err != nil {
		return ReverseResult{}, err
	}
	if posted.Replayed {
		if existingKey == "" {
			return ReverseResult{}, ErrDataIntegrity
		}
		return ReverseResult{Transaction: posted.Entry, RechargeTransactionID: originalID, Replayed: true}, nil
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO recharge_reversals
		(recharge_transaction_id, reversal_transaction_id, administrator_id, reason, created_at)
		VALUES (?, ?, ?, ?, ?)`, originalID, posted.Entry.ID, actorID, input.Reason,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return ReverseResult{}, err
	}
	reversalID, err := result.LastInsertId()
	if err != nil {
		return ReverseResult{}, err
	}
	if err := store.RecordAudit(ctx, tx, actorID, "RECHARGE_REVERSED", "recharge_reversal",
		strconv.FormatInt(reversalID, 10), map[string]any{
			"original_transaction_id": originalID, "reversal_transaction_id": posted.Entry.ID,
			"receipt_id": receiptID, "amount_cents": amount,
		}); err != nil {
		return ReverseResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReverseResult{}, err
	}
	return ReverseResult{Transaction: posted.Entry, RechargeTransactionID: originalID}, nil
}

func findReceipt(ctx context.Context, tx *sql.Tx, transactionID int64) (Receipt, error) {
	var receipt Receipt
	var collectedAt string
	err := tx.QueryRowContext(ctx, `SELECT id, receipt_ref, employee_id, amount_cents, collected_at,
		payment_method, recorded_by, recharge_transaction_id FROM recharge_receipts
		WHERE recharge_transaction_id = ?`, transactionID).Scan(&receipt.ID, &receipt.ReceiptRef,
		&receipt.EmployeeID, &receipt.AmountCents, &collectedAt, &receipt.PaymentMethod,
		&receipt.RecordedBy, &receipt.RechargeTransactionID)
	if errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, ErrDataIntegrity
	}
	if err != nil {
		return Receipt{}, err
	}
	receipt.CollectedAt, err = time.Parse(time.RFC3339Nano, collectedAt)
	if err != nil {
		return Receipt{}, ErrDataIntegrity
	}
	return receipt, nil
}

func validText(value string, max int) bool {
	return len(value) <= max && strings.TrimSpace(value) != ""
}
