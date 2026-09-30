package paymenttokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/employees"
	"github.com/EziosWJ/canteen-wallet/backend/internal/settings"
)

var (
	ErrSessionInvalid          = errors.New("employee session is invalid")
	ErrPasswordChangeRequired  = errors.New("temporary password must be changed")
	ErrAccountUnavailable      = errors.New("account is unavailable for consumption")
	ErrTokenInvalid            = errors.New("payment token is invalid")
	ErrTokenExpired            = errors.New("payment token has expired")
	ErrTokenRevoked            = errors.New("payment token is revoked")
	ErrTokenProcessed          = errors.New("payment token was already processed")
	ErrRefreshTooSoon          = errors.New("payment token refresh requested too soon")
	ErrPresentationUnavailable = errors.New("payment presentation unavailable")
	ErrEntranceDisabled        = errors.New("payment code entrance is disabled")
)

const (
	refreshInterval = 30 * time.Second
	validWindow     = 60 * time.Second
)

type Token struct {
	Value          string    `json:"token"`
	PresentationID string    `json:"presentation_id"`
	ServerTime     time.Time `json:"server_time"`
	RefreshAfter   time.Time `json:"refresh_after"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type Record struct {
	ID                 int64
	EmployeeID         int64
	SessionID          int64
	AccountID          int64
	TransactionID      int64
	PresentationID     string
	ResultCode         string
	ResultMessage      string
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

type Service struct {
	db    *sql.DB
	modes ModesReader
}

// ModesReader reads the enabled consumption entrances inside the caller's
// transaction, so issuing a payment code observes the same configuration as the
// scan that will charge it.
type ModesReader interface {
	ModesTx(ctx context.Context, tx *sql.Tx) (settings.Modes, error)
}

func New(db *sql.DB, modes ModesReader) *Service { return &Service{db: db, modes: modes} }

func (s *Service) Issue(ctx context.Context, principal employees.Principal, presentationID string) (Token, error) {
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
	if s.modes != nil {
		modes, err := s.modes.ModesTx(ctx, tx)
		if err != nil {
			return Token{}, err
		}
		// A disabled payment-code entrance hands out no new scannable code. The
		// employee page hides the entry as well, but this check is authoritative.
		if !modes.PaymentCode {
			return Token{}, ErrEntranceDisabled
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	requestedNew := presentationID == ""
	if presentationID == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return Token{}, err
		}
		presentationID = "prs_" + hex.EncodeToString(raw[:])
	} else if len(presentationID) != 36 || presentationID[:4] != "prs_" {
		return Token{}, ErrPresentationUnavailable
	} else if _, err := hex.DecodeString(presentationID[4:]); err != nil {
		return Token{}, ErrPresentationUnavailable
	}
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
	if requestedNew {
		var activeID string
		err := tx.QueryRowContext(ctx, `SELECT id FROM payment_presentations
			WHERE session_id=? AND state='ACTIVE' ORDER BY rowid DESC LIMIT 1`,
			principal.SessionID).Scan(&activeID)
		if err == nil {
			presentationID = activeID
		} else if !errors.Is(err, sql.ErrNoRows) {
			return Token{}, err
		}
	}
	var presentationState string
	err = tx.QueryRowContext(ctx, `SELECT state FROM payment_presentations
		WHERE id=? AND employee_id=? AND session_id=?`, presentationID, principal.ID, principal.SessionID).Scan(&presentationState)
	if errors.Is(err, sql.ErrNoRows) {
		var idUsed int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_presentations WHERE id=?`, presentationID).Scan(&idUsed); err != nil {
			return Token{}, err
		}
		if idUsed > 0 {
			return Token{}, ErrPresentationUnavailable
		}
		var otherPending int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_consumptions p
			JOIN payment_tokens t ON t.id=p.token_id WHERE t.session_id=? AND p.state='PENDING' AND p.expires_at>?`,
			principal.SessionID, now.Unix()).Scan(&otherPending); err != nil {
			return Token{}, err
		}
		if otherPending > 0 {
			return Token{}, ErrPresentationUnavailable
		}
		if _, err := tx.ExecContext(ctx, `UPDATE payment_presentations SET state='FAILED',result_code='SUPERSEDED'
			WHERE session_id=? AND state='ACTIVE'`, principal.SessionID); err != nil {
			return Token{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO payment_presentations(id,employee_id,session_id,created_at)
			VALUES (?,?,?,?)`, presentationID, principal.ID, principal.SessionID, now.Format(time.RFC3339Nano)); err != nil {
			return Token{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE payment_tokens SET state='REVOKED'
			WHERE session_id=? AND state='ACTIVE' AND (presentation_id IS NULL OR presentation_id!=?)`, principal.SessionID, presentationID); err != nil {
			return Token{}, err
		}
	} else if err != nil {
		return Token{}, err
	} else if presentationState != "ACTIVE" {
		return Token{}, ErrPresentationUnavailable
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_consumptions p
		JOIN payment_tokens t ON t.id=p.token_id WHERE t.presentation_id=? AND p.state='PENDING'`, presentationID).Scan(&pending); err != nil {
		return Token{}, err
	}
	if pending > 0 {
		return Token{}, ErrPresentationUnavailable
	}
	if _, err := tx.ExecContext(ctx, `UPDATE payment_tokens SET state = 'EXPIRED'
		WHERE session_id = ? AND state = 'ACTIVE' AND expires_at <= ?`, principal.SessionID, now.Unix()); err != nil {
		return Token{}, err
	}
	var lastIssued int64
	err = tx.QueryRowContext(ctx, `SELECT issued_at FROM payment_tokens
		WHERE session_id = ? AND presentation_id=? AND state = 'ACTIVE' ORDER BY id DESC LIMIT 1`, principal.SessionID, presentationID).Scan(&lastIssued)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Token{}, err
	}
	if err == nil && now.Unix() < lastIssued+int64(refreshInterval/time.Second) {
		return Token{PresentationID: presentationID, ServerTime: time.Now().UTC(), RefreshAfter: time.Unix(lastIssued, 0).UTC().Add(refreshInterval)}, ErrRefreshTooSoon
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Token{}, err
	}
	value := "pmt_" + base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(value))
	_, err = tx.ExecContext(ctx, `INSERT INTO payment_tokens
		(token_hash, employee_id, session_id, issued_at, expires_at, presentation_id)
		VALUES (?, ?, ?, ?, ?, ?)`, hash[:], principal.ID, principal.SessionID, now.Unix(), now.Add(validWindow).Unix(), presentationID)
	if err != nil {
		return Token{}, err
	}
	if err := tx.Commit(); err != nil {
		return Token{}, err
	}
	return Token{Value: value, PresentationID: presentationID, ServerTime: time.Now().UTC(), RefreshAfter: now.Add(refreshInterval), ExpiresAt: now.Add(validWindow)}, nil
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
		p.transaction_id, COALESCE(p.presentation_id,''), COALESCE(p.result_code,''), COALESCE(p.result_message,'')
		FROM payment_tokens p
		JOIN employee_sessions s ON s.id = p.session_id AND s.employee_id = p.employee_id
		JOIN employees e ON e.id = p.employee_id
		JOIN accounts a ON a.employee_id = e.id
		WHERE p.token_hash = ?`, hash[:]).Scan(&record.ID, &record.EmployeeID, &record.SessionID,
		&record.AccountID, &record.State, &expiresAt, &sessionExpiresAt, &revokedAt,
		&record.EmployeeStatus, &record.AccountStatus, &mustChange, &transactionID,
		&record.PresentationID, &record.ResultCode, &record.ResultMessage)
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
