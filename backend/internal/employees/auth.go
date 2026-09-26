package employees

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"strconv"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/adminauth"
	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
)

var (
	ErrInvalidCredentials = errors.New("invalid employee credentials")
	ErrUnauthenticated    = errors.New("employee session required")
)

const employeeSessionDuration = 30 * 24 * time.Hour

type Principal struct {
	ID                 int64
	SessionID          int64
	Status             string
	AccountStatus      string
	MustChangePassword bool
	tokenHash          [32]byte
}

// CanIssuePaymentToken is the common gate for the later payment-token API.
// The token must also remain tied to SessionID and be rejected after revocation.
func (p Principal) CanIssuePaymentToken() bool {
	return !p.MustChangePassword && p.Status == "ACTIVE" && p.AccountStatus == "ACTIVE"
}

type Session struct {
	Token              string
	ExpiresAt          time.Time
	EmployeeID         int64
	MustChangePassword bool
}

func (s *Service) Login(ctx context.Context, phone, password string) (Session, error) {
	phone = normalizePhone(phone)
	if !phonePattern.MatchString(phone) || len(password) > 1024 {
		return Session{}, ErrInvalidCredentials
	}
	select {
	case s.loginGate <- struct{}{}:
		defer func() { <-s.loginGate }()
	case <-ctx.Done():
		return Session{}, ctx.Err()
	}
	var id int64
	var hash, status, accountStatus string
	err := s.db.QueryRowContext(ctx, `SELECT e.id, e.password_hash, e.status, a.status FROM employees e
		JOIN accounts a ON a.employee_id = e.id WHERE e.phone = ?`, phone).Scan(&id, &hash, &status, &accountStatus)
	if errors.Is(err, sql.ErrNoRows) {
		adminauth.SpendPasswordCheck(password)
		return Session{}, waitFailedEmployeeLogin(ctx)
	}
	if err != nil {
		return Session{}, err
	}
	if !adminauth.VerifyPassword(password, hash) || status == "CLOSED" || accountStatus == "CLOSED" {
		return Session{}, waitFailedEmployeeLogin(ctx)
	}
	token, tokenHash, err := newSessionToken()
	if err != nil {
		return Session{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	expires := now.Add(employeeSessionDuration)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	if _, err := RevokeSessions(ctx, tx, id); err != nil {
		return Session{}, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO employee_sessions (employee_id, token_hash, created_at, expires_at)
		SELECT e.id, ?, ?, ? FROM employees e JOIN accounts a ON a.employee_id = e.id
		WHERE e.id = ? AND e.password_hash = ? AND e.status != 'CLOSED' AND a.status != 'CLOSED'`,
		tokenHash[:], now.Unix(), expires.Unix(), id, hash)
	if err != nil {
		return Session{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Session{}, err
	}
	if count != 1 {
		return Session{}, ErrInvalidCredentials
	}
	var mustChange int
	if err := tx.QueryRowContext(ctx, `SELECT must_change_password FROM employees WHERE id = ?`, id).Scan(&mustChange); err != nil {
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, err
	}
	return Session{Token: token, ExpiresAt: expires, EmployeeID: id, MustChangePassword: mustChange == 1}, nil
}

func waitFailedEmployeeLogin(ctx context.Context) error {
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ErrInvalidCredentials
	}
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return Principal{}, ErrUnauthenticated
	}
	hash := sha256.Sum256(raw)
	var principal Principal
	var mustChange int
	err = s.db.QueryRowContext(ctx, `SELECT e.id, s.id, e.status, a.status, e.must_change_password
		FROM employee_sessions s JOIN employees e ON e.id = s.employee_id
		JOIN accounts a ON a.employee_id = e.id
		WHERE s.token_hash = ? AND s.revoked_at IS NULL AND s.expires_at > ?
		AND e.status != 'CLOSED' AND a.status != 'CLOSED'`, hash[:], time.Now().Unix()).Scan(
		&principal.ID, &principal.SessionID, &principal.Status, &principal.AccountStatus, &mustChange)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	principal.MustChangePassword = mustChange == 1
	principal.tokenHash = hash
	return principal, nil
}

func (s *Service) ChangePassword(ctx context.Context, principal Principal, oldPassword, newPassword string) (Session, error) {
	if len(oldPassword) > 1024 {
		return Session{}, ErrInvalidCredentials
	}
	var oldHash string
	err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM employees WHERE id = ?`, principal.ID).Scan(&oldHash)
	if err != nil {
		return Session{}, err
	}
	if !adminauth.VerifyPassword(oldPassword, oldHash) {
		return Session{}, ErrInvalidCredentials
	}
	newHash, err := adminauth.HashPassword(newPassword)
	if err != nil {
		return Session{}, err
	}
	token, tokenHash, err := newSessionToken()
	if err != nil {
		return Session{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	expires := now.Add(employeeSessionDuration)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE employee_sessions SET revoked_at = ? WHERE id = ?
		AND employee_id = ? AND token_hash = ? AND revoked_at IS NULL AND expires_at > ?`,
		now.Unix(), principal.SessionID, principal.ID, principal.tokenHash[:], now.Unix())
	if err != nil {
		return Session{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Session{}, err
	}
	if count != 1 {
		return Session{}, ErrUnauthenticated
	}
	result, err = tx.ExecContext(ctx, `UPDATE employees SET password_hash = ?, must_change_password = 0, updated_at = ?
		WHERE id = ? AND password_hash = ? AND status != 'CLOSED'`, newHash, now.Format(time.RFC3339Nano), principal.ID, oldHash)
	if err != nil {
		return Session{}, err
	}
	count, err = result.RowsAffected()
	if err != nil {
		return Session{}, err
	}
	if count != 1 {
		return Session{}, ErrUnauthenticated
	}
	result, err = tx.ExecContext(ctx, `INSERT INTO employee_sessions (employee_id, token_hash, created_at, expires_at)
		VALUES (?, ?, ?, ?)`, principal.ID, tokenHash[:], now.Unix(), expires.Unix())
	if err != nil {
		return Session{}, err
	}
	if _, err := result.LastInsertId(); err != nil {
		return Session{}, err
	}
	if err := store.RecordAudit(ctx, tx, 0, "EMPLOYEE_PASSWORD_CHANGED", "employee", strconv.FormatInt(principal.ID, 10),
		map[string]int64{"session_id": principal.SessionID}); err != nil {
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, err
	}
	return Session{Token: token, ExpiresAt: expires, EmployeeID: principal.ID, MustChangePassword: false}, nil
}

func (s *Service) Logout(ctx context.Context, principal Principal) error {
	result, err := s.db.ExecContext(ctx, `UPDATE employee_sessions SET revoked_at = ? WHERE id = ?
		AND employee_id = ? AND token_hash = ? AND revoked_at IS NULL`, time.Now().Unix(),
		principal.SessionID, principal.ID, principal.tokenHash[:])
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrUnauthenticated
	}
	return nil
}

func newSessionToken() (string, [32]byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", [32]byte{}, err
	}
	return base64.RawURLEncoding.EncodeToString(raw), sha256.Sum256(raw), nil
}
