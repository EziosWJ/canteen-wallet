package paymenttokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/employees"
)

var (
	ErrSessionInvalid         = errors.New("employee session is invalid")
	ErrPasswordChangeRequired = errors.New("temporary password must be changed")
	ErrAccountUnavailable     = errors.New("account is unavailable for consumption")
	ErrTokenInvalid           = errors.New("payment token is invalid")
	ErrTokenExpired           = errors.New("payment token has expired")
	ErrTokenRevoked           = errors.New("payment token is revoked")
	ErrTokenProcessed         = errors.New("payment token was already processed")
	ErrRefreshTooSoon         = errors.New("payment token refresh requested too soon")
)

const (
	refreshInterval = 30 * time.Second
	validWindow     = 60 * time.Second
)

type Token struct {
	Value        string    `json:"token"`
	RefreshAfter time.Time `json:"refresh_after"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type Record struct {
	ID                 int64
	EmployeeID         int64
	SessionID          int64
	AccountID          int64
	TransactionID      int64
	State              string
	ExpiresAt          time.Time
	SessionExpiresAt   time.Time
	SessionRevoked     bool
	EmployeeStatus     string
	AccountStatus      string
	MustChangePassword bool
}

// FirstUseAllowed checks a token before the first funds transaction. A
// processed token needs separate replay handling in the later consume flow.
func (record Record) FirstUseAllowed(at time.Time) error {
	if record.State == "PROCESSED" {
		return ErrTokenProcessed
	}
	if record.State == "REVOKED" || record.SessionRevoked || !at.Before(record.SessionExpiresAt) {
		return ErrTokenRevoked
	}
	if record.State == "EXPIRED" || !at.Before(record.ExpiresAt) {
		return ErrTokenExpired
	}
	if record.State != "ACTIVE" {
		return ErrTokenInvalid
	}
	if record.MustChangePassword {
		return ErrPasswordChangeRequired
	}
	if record.EmployeeStatus != "ACTIVE" || record.AccountStatus != "ACTIVE" {
		return ErrAccountUnavailable
	}
	return nil
}

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

func (s *Service) Issue(ctx context.Context, principal employees.Principal) (Token, error) {
	if principal.MustChangePassword {
		return Token{}, ErrPasswordChangeRequired
	}
	if !principal.CanIssuePaymentToken() {
		return Token{}, ErrAccountUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Token{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Truncate(time.Second)
	var mustChange int
	var employeeStatus, accountStatus string
	err = tx.QueryRowContext(ctx, `SELECT e.must_change_password, e.status, a.status
		FROM employee_sessions s JOIN employees e ON e.id = s.employee_id
		JOIN accounts a ON a.employee_id = e.id
		WHERE s.id = ? AND s.employee_id = ? AND s.revoked_at IS NULL AND s.expires_at > ?`,
		principal.SessionID, principal.ID, now.Unix()).Scan(&mustChange, &employeeStatus, &accountStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrSessionInvalid
	}
	if err != nil {
		return Token{}, err
	}
	if mustChange == 1 {
		return Token{}, ErrPasswordChangeRequired
	}
	if employeeStatus != "ACTIVE" || accountStatus != "ACTIVE" {
		return Token{}, ErrAccountUnavailable
	}
	if _, err := tx.ExecContext(ctx, `UPDATE payment_tokens SET state = 'EXPIRED'
		WHERE session_id = ? AND state = 'ACTIVE' AND expires_at <= ?`, principal.SessionID, now.Unix()); err != nil {
		return Token{}, err
	}
	var lastIssued int64
	err = tx.QueryRowContext(ctx, `SELECT issued_at FROM payment_tokens
		WHERE session_id = ? AND state = 'ACTIVE' ORDER BY id DESC LIMIT 1`, principal.SessionID).Scan(&lastIssued)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Token{}, err
	}
	if err == nil && now.Unix() < lastIssued+int64(refreshInterval/time.Second) {
		return Token{RefreshAfter: time.Unix(lastIssued, 0).UTC().Add(refreshInterval)}, ErrRefreshTooSoon
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Token{}, err
	}
	value := "pmt_" + base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(value))
	_, err = tx.ExecContext(ctx, `INSERT INTO payment_tokens
		(token_hash, employee_id, session_id, issued_at, expires_at)
		VALUES (?, ?, ?, ?, ?)`, hash[:], principal.ID, principal.SessionID, now.Unix(), now.Add(validWindow).Unix())
	if err != nil {
		return Token{}, err
	}
	if err := tx.Commit(); err != nil {
		return Token{}, err
	}
	return Token{Value: value, RefreshAfter: now.Add(refreshInterval), ExpiresAt: now.Add(validWindow)}, nil
}

// LookupTx returns the stored result state without accepting the token for a
// new consumption. CW-09 must handle PROCESSED replay before first-use checks.
func LookupTx(ctx context.Context, tx *sql.Tx, value string) (Record, error) {
	hash, err := tokenHash(value)
	if err != nil {
		return Record{}, err
	}
	var record Record
	var expiresAt, sessionExpiresAt int64
	var revokedAt sql.NullInt64
	var transactionID sql.NullInt64
	var mustChange int
	err = tx.QueryRowContext(ctx, `SELECT p.id, p.employee_id, p.session_id, a.id, p.state,
		p.expires_at, s.expires_at, s.revoked_at, e.status, a.status, e.must_change_password,
		p.transaction_id
		FROM payment_tokens p
		JOIN employee_sessions s ON s.id = p.session_id AND s.employee_id = p.employee_id
		JOIN employees e ON e.id = p.employee_id
		JOIN accounts a ON a.employee_id = e.id
		WHERE p.token_hash = ?`, hash[:]).Scan(&record.ID, &record.EmployeeID, &record.SessionID,
		&record.AccountID, &record.State, &expiresAt, &sessionExpiresAt, &revokedAt,
		&record.EmployeeStatus, &record.AccountStatus, &mustChange, &transactionID)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrTokenInvalid
	}
	if err != nil {
		return Record{}, err
	}
	record.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	record.SessionExpiresAt = time.Unix(sessionExpiresAt, 0).UTC()
	record.SessionRevoked = revokedAt.Valid
	record.MustChangePassword = mustChange == 1
	record.TransactionID = transactionID.Int64
	return record, nil
}

func LookupForFirstUseTx(ctx context.Context, tx *sql.Tx, value string, at time.Time) (Record, error) {
	record, err := LookupTx(ctx, tx, value)
	if err != nil {
		return Record{}, err
	}
	if err := record.FirstUseAllowed(at); err != nil {
		return Record{}, err
	}
	return record, nil
}

func tokenHash(value string) ([32]byte, error) {
	if len(value) != 47 || value[:4] != "pmt_" {
		return [32]byte{}, ErrTokenInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(value[4:])
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != value[4:] {
		return [32]byte{}, ErrTokenInvalid
	}
	return sha256.Sum256([]byte(value)), nil
}
