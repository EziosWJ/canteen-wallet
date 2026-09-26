package adminauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
)

var (
	ErrInvalidUsername    = errors.New("username must be 3 to 64 letters, digits, dots, underscores or hyphens")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnauthenticated    = errors.New("unauthenticated")
	usernamePattern       = regexp.MustCompile(`^[a-z0-9._-]{3,64}$`)
)

const sessionDuration = 12 * time.Hour
const failedLoginDelay = 500 * time.Millisecond

// SecondFactor is the extension point for mandatory administrator MFA before
// the live pilot. When configured, a password alone cannot issue a session.
type SecondFactor interface {
	Verify(ctx context.Context, administratorID int64, proof string) error
}

type Service struct {
	db        *sql.DB
	factor    SecondFactor
	loginGate chan struct{}
}

type Principal struct {
	ID        int64
	Username  string
	tokenHash [32]byte
}

type Session struct {
	Token         string
	ExpiresAt     time.Time
	Administrator Principal
}

func New(db *sql.DB, factor SecondFactor) *Service {
	return &Service{db: db, factor: factor, loginGate: make(chan struct{}, 2)}
}

func (s *Service) CreateAdmin(ctx context.Context, username, password string) (int64, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernamePattern.MatchString(username) {
		return 0, ErrInvalidUsername
	}
	hash, err := HashPassword(password)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var existingAdmins int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM administrators").Scan(&existingAdmins); err != nil {
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `INSERT INTO administrators (username, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?)`, username, hash, now, now)
	if err != nil {
		return 0, fmt.Errorf("create administrator: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	osUsername := "unknown"
	if current, err := user.Current(); err == nil {
		osUsername = current.Username
	}
	if err := store.RecordAudit(ctx, tx, 0, "ADMIN_ACCOUNT_CREATED", "administrator", strconv.FormatInt(id, 10),
		map[string]any{"source": "cli", "os_uid": os.Getuid(), "os_username": osUsername, "bootstrap": existingAdmins == 0}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Service) Login(ctx context.Context, username, password, secondFactorProof string) (Session, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if len(username) > 64 || len(password) > 1024 || len(secondFactorProof) > 128 {
		return Session{}, ErrInvalidCredentials
	}
	select {
	case s.loginGate <- struct{}{}:
		defer func() { <-s.loginGate }()
	case <-ctx.Done():
		return Session{}, ctx.Err()
	}
	var id int64
	var hash, status string
	err := s.db.QueryRowContext(ctx, `SELECT id, password_hash, status FROM administrators WHERE username = ?`, username).Scan(&id, &hash, &status)
	if errors.Is(err, sql.ErrNoRows) {
		SpendPasswordCheck(password)
		return Session{}, s.failLoginWithDelay(ctx, 0, "invalid_credentials")
	}
	if err != nil {
		return Session{}, err
	}
	if !VerifyPassword(password, hash) || status != "active" {
		return Session{}, s.failLoginWithDelay(ctx, id, "invalid_credentials")
	}
	if s.factor == nil && secondFactorProof != "" {
		return Session{}, s.failLoginWithDelay(ctx, id, "second_factor_unavailable")
	}
	if s.factor != nil {
		if err := s.factor.Verify(ctx, id, secondFactorProof); err != nil {
			return Session{}, s.failLoginWithDelay(ctx, id, "second_factor_failed")
		}
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return Session{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256(tokenBytes)
	now := time.Now().UTC()
	expiresAt := now.Add(sessionDuration)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO admin_sessions (token_hash, administrator_id, created_at, expires_at)
		SELECT ?, id, ?, ? FROM administrators WHERE id = ? AND password_hash = ? AND status = 'active'`,
		tokenHash[:], now.Unix(), expiresAt.Unix(), id, hash)
	if err != nil {
		return Session{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Session{}, err
	}
	if count != 1 {
		if err := tx.Rollback(); err != nil {
			return Session{}, err
		}
		return Session{}, s.failLoginWithDelay(ctx, id, "state_changed")
	}
	if err := store.RecordAudit(ctx, tx, id, "ADMIN_LOGIN_SUCCEEDED", "administrator", strconv.FormatInt(id, 10), nil); err != nil {
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, err
	}
	return Session{Token: token, ExpiresAt: expiresAt, Administrator: Principal{ID: id, Username: username, tokenHash: tokenHash}}, nil
}

func (s *Service) failLoginWithDelay(ctx context.Context, id int64, reason string) error {
	auditCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(auditCtx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	subjectID := ""
	if id > 0 {
		subjectID = strconv.FormatInt(id, 10)
	}
	if err := store.RecordAudit(auditCtx, tx, id, "ADMIN_LOGIN_FAILED", "administrator", subjectID, map[string]string{"reason": reason}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return waitFailedLogin(ctx)
}

func waitFailedLogin(ctx context.Context) error {
	timer := time.NewTimer(failedLoginDelay)
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
	err = s.db.QueryRowContext(ctx, `SELECT a.id, a.username FROM admin_sessions AS s
		JOIN administrators AS a ON a.id = s.administrator_id
		WHERE s.token_hash = ? AND s.revoked_at IS NULL AND s.expires_at > ? AND a.status = 'active'`, hash[:], time.Now().Unix()).Scan(&principal.ID, &principal.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	principal.tokenHash = hash
	return principal, nil
}

func (s *Service) Logout(ctx context.Context, principal Principal) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE admin_sessions SET revoked_at = ?
		WHERE token_hash = ? AND administrator_id = ? AND revoked_at IS NULL`, time.Now().Unix(), principal.tokenHash[:], principal.ID)
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
	if err := store.RecordAudit(ctx, tx, principal.ID, "ADMIN_LOGOUT", "administrator", strconv.FormatInt(principal.ID, 10), nil); err != nil {
		return err
	}
	return tx.Commit()
}
