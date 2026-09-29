package ledger

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
	"modernc.org/sqlite"
)

type Kind string

const (
	Recharge          Kind = "RECHARGE"
	RechargeReversal  Kind = "RECHARGE_REVERSAL"
	Consume           Kind = "CONSUME"
	Refund            Kind = "REFUND"
	BalanceAdjustment Kind = "BALANCE_ADJUSTMENT"
	BalanceWithdrawal Kind = "BALANCE_WITHDRAWAL"
)

var (
	ErrInvalidRequest      = errors.New("invalid fund transaction request")
	ErrAccountNotFound     = errors.New("account not found")
	ErrAccountUnavailable  = errors.New("account state does not allow transaction")
	ErrInsufficientFunds   = errors.New("insufficient funds")
	ErrBalanceOverflow     = errors.New("balance overflow")
	ErrIdempotencyConflict = errors.New("idempotency key used for different transaction")
	ErrDuplicateReference  = errors.New("related transaction already reversed or refunded")
	ErrInvalidReference    = errors.New("related transaction does not match account, type or amount")
	ErrBusinessConflict    = errors.New("business reference already used for a different transaction")
)

type Request struct {
	AccountID            int64
	Kind                 Kind
	Amount               int64 // Signed integer cents; positive adds funds, negative deducts.
	AdministratorID      int64
	TerminalID           string
	BusinessType         string
	BusinessID           string
	RelatedTransactionID int64
	IdempotencyKey       string
	Reason               string
}

type Entry struct {
	ID                   int64
	TransactionNo        string
	AccountID            int64
	Kind                 Kind
	Amount               int64
	BeforeBalance        int64
	AfterBalance         int64
	AdministratorID      int64
	TerminalID           string
	BusinessType         string
	BusinessID           string
	RelatedTransactionID int64
	IdempotencyKey       string
	Reason               string
	CreatedAt            time.Time
}

type Result struct {
	Entry    Entry
	Replayed bool
}

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

func (s *Service) Get(ctx context.Context, id int64) (Entry, error) {
	return scanEntry(s.db.QueryRowContext(ctx, "SELECT "+entryColumns+" FROM transactions WHERE id = ?", id))
}

func (s *Service) FindByIdempotencyKey(ctx context.Context, key string) (Entry, error) {
	return scanEntry(s.db.QueryRowContext(ctx, "SELECT "+entryColumns+" FROM transactions WHERE idempotency_key = ?", key))
}

// ListForEmployee reads only transactions on the authenticated employee's
// account, ordered newest first. beforeID is an exclusive pagination cursor.
func (s *Service) ListForEmployee(ctx context.Context, employeeID, beforeID int64, limit int) ([]Entry, bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+entryColumns+`
		FROM transactions
		WHERE account_id = (SELECT id FROM accounts WHERE employee_id = ?)
		AND (? = 0 OR id < ?)
		ORDER BY id DESC LIMIT ?`, employeeID, beforeID, beforeID, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := make([]Entry, 0, limit)
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, false, err
		}
		items = append(items, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	return items, more, nil
}

// Apply owns the transaction for one balance change and its audit event.
func (s *Service) Apply(ctx context.Context, request Request) (Result, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	result, err := ApplyInTx(ctx, tx, request)
	if err != nil {
		return Result{}, err
	}
	if err := tx.Commit(); err != nil {
		return Result{}, err
	}
	return result, nil
}

// ApplyInTx lets a business operation write its own records, funds and audit
// in one transaction. Check Replayed before making any new business writes.
// Only trusted server code should construct Request; never expose it directly
// as an HTTP payload because amount and account selection belong to the server.
func ApplyInTx(ctx context.Context, tx *sql.Tx, request Request) (Result, error) {
	if err := validate(request); err != nil {
		return Result{}, err
	}
	previous, err := getByIdempotencyKey(ctx, tx, request.IdempotencyKey)
	if err == nil {
		if !sameIntent(previous, request) {
			return Result{}, ErrIdempotencyConflict
		}
		return Result{Entry: previous, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, err
	}
	if singleUseBusiness(request) {
		previous, err := getByBusinessKey(ctx, tx, request.Kind, request.BusinessType, request.BusinessID)
		if err == nil {
			if !sameIntent(previous, request) {
				return Result{}, ErrBusinessConflict
			}
			return Result{Entry: previous, Replayed: true}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Result{}, err
		}
	}
	var balance int64
	var status string
	err = tx.QueryRowContext(ctx, `SELECT balance, status FROM accounts WHERE id = ?`, request.AccountID).Scan(&balance, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, ErrAccountNotFound
	}
	if err != nil {
		return Result{}, err
	}
	if status == "CLOSED" && request.Kind != Refund && request.Kind != BalanceWithdrawal {
		return Result{}, ErrAccountUnavailable
	}
	if request.Kind == Consume && status != "ACTIVE" {
		return Result{}, ErrAccountUnavailable
	}
	if status == "CLOSED" && request.Kind == BalanceWithdrawal && request.RelatedTransactionID == 0 {
		return Result{}, ErrInvalidReference
	}
	if err := validateReference(ctx, tx, request); err != nil {
		return Result{}, err
	}
	if request.Amount > 0 && balance > math.MaxInt64-request.Amount {
		return Result{}, ErrBalanceOverflow
	}
	if request.Amount < 0 && (request.Amount == math.MinInt64 || balance < -request.Amount) {
		return Result{}, ErrInsufficientFunds
	}
	after := balance + request.Amount
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Result{}, err
	}
	transactionNo := "txn_" + hex.EncodeToString(random[:])
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `INSERT INTO transactions
		(transaction_no, account_id, type, amount, before_balance, after_balance,
		 administrator_id, terminal_id, business_type, business_id, related_transaction_id,
		 idempotency_key, reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		transactionNo, request.AccountID, request.Kind, request.Amount, balance, after,
		nullID(request.AdministratorID), nullString(request.TerminalID), request.BusinessType,
		request.BusinessID, nullID(request.RelatedTransactionID), request.IdempotencyKey,
		nullString(request.Reason), now.Format(time.RFC3339Nano))
	if err != nil {
		return Result{}, classifyInsert(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Result{}, err
	}
	entry, err := getByID(ctx, tx, id)
	if err != nil {
		return Result{}, err
	}
	if err := store.RecordAudit(ctx, tx, request.AdministratorID, "FUND_TRANSACTION_POSTED", "transaction",
		strconv.FormatInt(id, 10), map[string]any{"type": request.Kind, "account_id": request.AccountID,
			"business_type": request.BusinessType, "business_id": request.BusinessID,
			"terminal_id": request.TerminalID}); err != nil {
		return Result{}, err
	}
	return Result{Entry: entry}, nil
}

func validate(request Request) error {
	// A transaction has exactly one actor, except self-service consumption: the
	// employee confirms it, so it carries neither an administrator nor a terminal.
	selfService := request.BusinessType == "SELF_SERVICE"
	actorOK := (request.AdministratorID > 0) != (request.TerminalID != "")
	if selfService {
		actorOK = request.AdministratorID == 0 && request.TerminalID == ""
	}
	if request.AccountID < 1 || request.Amount == 0 || request.Amount == math.MinInt64 ||
		!validText(request.BusinessType, 64) || !validText(request.BusinessID, 128) ||
		!validText(request.IdempotencyKey, 128) || len(request.Reason) > 512 ||
		!actorOK ||
		(request.AdministratorID < 0) || (request.TerminalID != "" && !validText(request.TerminalID, 64)) ||
		request.RelatedTransactionID < 0 {
		return ErrInvalidRequest
	}
	if request.Kind == BalanceAdjustment && strings.TrimSpace(request.Reason) == "" {
		return ErrInvalidRequest
	}
	if (request.Kind == Refund || request.Kind == RechargeReversal) && request.RelatedTransactionID == 0 {
		return ErrInvalidRequest
	}
	if request.Kind != Refund && request.Kind != RechargeReversal && request.Kind != BalanceWithdrawal && request.RelatedTransactionID != 0 {
		return ErrInvalidRequest
	}
	if request.Kind != Consume && request.AdministratorID == 0 {
		return ErrInvalidRequest
	}
	switch request.Kind {
	case Recharge, Refund:
		if request.Amount < 0 {
			return ErrInvalidRequest
		}
		if request.Kind == Recharge && request.BusinessType != "RECEIPT" {
			return ErrInvalidRequest
		}
		if request.Kind == Refund && request.BusinessType != "CONSUME" {
			return ErrInvalidRequest
		}
	case RechargeReversal, Consume, BalanceWithdrawal:
		if request.Amount > 0 {
			return ErrInvalidRequest
		}
		if request.Kind == RechargeReversal && request.BusinessType != "RECHARGE" {
			return ErrInvalidRequest
		}
		if request.Kind == Consume && request.TerminalID != "" && request.BusinessType != "MEAL_PERIOD" {
			return ErrInvalidRequest
		}
		if request.Kind == Consume && request.AdministratorID > 0 && request.BusinessType != "MANUAL_SUPPLY" {
			return ErrInvalidRequest
		}
		// Self-service consumption is confirmed by the employee, so it belongs to
		// neither a terminal nor an administrator.
		if request.Kind == Consume && request.TerminalID == "" && request.AdministratorID == 0 && request.BusinessType != "SELF_SERVICE" {
			return ErrInvalidRequest
		}
		if request.Kind == BalanceWithdrawal && request.BusinessType != "PAYOUT_RECEIPT" {
			return ErrInvalidRequest
		}
	case BalanceAdjustment:
		if request.BusinessType != "ADJUSTMENT_CASE" {
			return ErrInvalidRequest
		}
	default:
		return ErrInvalidRequest
	}
	return nil
}

func validateReference(ctx context.Context, tx *sql.Tx, request Request) error {
	if request.RelatedTransactionID == 0 {
		return nil
	}
	var originalAccountID, originalAmount int64
	var originalKind Kind
	err := tx.QueryRowContext(ctx, `SELECT account_id, type, amount FROM transactions WHERE id = ?`,
		request.RelatedTransactionID).Scan(&originalAccountID, &originalKind, &originalAmount)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidReference
	}
	if err != nil {
		return err
	}
	if originalAccountID != request.AccountID || originalAmount == math.MinInt64 || request.Amount != -originalAmount {
		return ErrInvalidReference
	}
	switch request.Kind {
	case Refund:
		if originalKind != Consume {
			return ErrInvalidReference
		}
	case RechargeReversal:
		if originalKind != Recharge {
			return ErrInvalidReference
		}
	case BalanceWithdrawal:
		if originalKind != Refund {
			return ErrInvalidReference
		}
	default:
		return ErrInvalidReference
	}
	return nil
}

func validText(value string, max int) bool {
	return len(value) <= max && strings.TrimSpace(value) != ""
}

func sameIntent(entry Entry, request Request) bool {
	return entry.AccountID == request.AccountID && entry.Kind == request.Kind && entry.Amount == request.Amount &&
		entry.AdministratorID == request.AdministratorID && entry.TerminalID == request.TerminalID &&
		entry.BusinessType == request.BusinessType && entry.BusinessID == request.BusinessID &&
		entry.RelatedTransactionID == request.RelatedTransactionID && entry.Reason == request.Reason
}

func singleUseBusiness(request Request) bool {
	return request.Kind == Recharge || request.Kind == BalanceWithdrawal ||
		request.Kind == BalanceAdjustment || (request.Kind == Consume && request.BusinessType == "MANUAL_SUPPLY")
}

func classifyInsert(err error) error {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code() == 2067 {
		return ErrDuplicateReference
	}
	return fmt.Errorf("insert fund transaction: %w", err)
}

func nullID(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

type scanner interface{ Scan(dest ...any) error }

const entryColumns = `id, transaction_no, account_id, type, amount, before_balance, after_balance,
	administrator_id, terminal_id, business_type, business_id, related_transaction_id,
	idempotency_key, reason, created_at`

func getByIdempotencyKey(ctx context.Context, tx *sql.Tx, key string) (Entry, error) {
	return scanEntry(tx.QueryRowContext(ctx, "SELECT "+entryColumns+" FROM transactions WHERE idempotency_key = ?", key))
}

func getByBusinessKey(ctx context.Context, tx *sql.Tx, kind Kind, businessType, businessID string) (Entry, error) {
	return scanEntry(tx.QueryRowContext(ctx, "SELECT "+entryColumns+" FROM transactions WHERE type = ? AND business_type = ? AND business_id = ?",
		kind, businessType, businessID))
}

func getByID(ctx context.Context, tx *sql.Tx, id int64) (Entry, error) {
	return scanEntry(tx.QueryRowContext(ctx, "SELECT "+entryColumns+" FROM transactions WHERE id = ?", id))
}

func scanEntry(row scanner) (Entry, error) {
	var entry Entry
	var administratorID, relatedID sql.NullInt64
	var terminalID, reason sql.NullString
	var createdAt string
	err := row.Scan(&entry.ID, &entry.TransactionNo, &entry.AccountID, &entry.Kind, &entry.Amount,
		&entry.BeforeBalance, &entry.AfterBalance, &administratorID, &terminalID, &entry.BusinessType,
		&entry.BusinessID, &relatedID, &entry.IdempotencyKey, &reason, &createdAt)
	if err != nil {
		return Entry{}, err
	}
	entry.AdministratorID = administratorID.Int64
	entry.TerminalID = terminalID.String
	entry.RelatedTransactionID = relatedID.Int64
	entry.Reason = reason.String
	entry.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Entry{}, err
	}
	return entry, nil
}
