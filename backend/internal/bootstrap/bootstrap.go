// Package bootstrap creates the first administrator. Until any administrator
// exists the public initialization page is available, so a fresh deployment does
// not need command-line access. Once one administrator exists the entry is
// permanently refused.
package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/adminauth"
	"github.com/EziosWJ/canteen-wallet/backend/internal/settings"
	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
)

var (
	// ErrAlreadyInitialized is returned when any administrator already exists.
	ErrAlreadyInitialized = errors.New("administrators already exist")
	// ErrInvalidInput covers a malformed username, a weak password or modes
	// that would leave every consumption entrance disabled.
	ErrInvalidInput = errors.New("invalid initialization request")
)

type Service struct {
	db       *sql.DB
	settings *settings.Service
}

func New(db *sql.DB, settingsService *settings.Service) *Service {
	return &Service{db: db, settings: settingsService}
}

// Required reports whether the initialization entry may still be used.
func (s *Service) Required(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM administrators`).Scan(&count); err != nil {
		return false, err
	}
	return count == 0, nil
}

// Initialize creates the first administrator together with the chosen
// consumption modes, the audit trail and the creator's own first session, all in
// a single transaction. The SQLite write lock serializes concurrent callers, so
// at most one observes an empty administrator table and succeeds; every other
// caller is refused and leaves no partial account, mode configuration or session
// behind, and a failure after the account is written rolls the account back
// rather than stranding an administrator nobody can sign in to.
func (s *Service) Initialize(ctx context.Context, username, password string, modes settings.Modes) (adminauth.Session, error) {
	username = adminauth.NormalizeUsername(username)
	if !adminauth.ValidUsername(username) || !modes.Valid() {
		return adminauth.Session{}, ErrInvalidInput
	}
	hash, err := adminauth.HashPassword(password)
	if err != nil {
		return adminauth.Session{}, ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return adminauth.Session{}, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM administrators`).Scan(&count); err != nil {
		return adminauth.Session{}, err
	}
	if count != 0 {
		return adminauth.Session{}, ErrAlreadyInitialized
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `INSERT INTO administrators (username, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?)`, username, hash, now, now)
	if err != nil {
		return adminauth.Session{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return adminauth.Session{}, err
	}
	if err := s.settings.SetTx(ctx, tx, 0, modes); err != nil {
		return adminauth.Session{}, err
	}
	if err := store.RecordAudit(ctx, tx, 0, "ADMIN_SYSTEM_INITIALIZED", "administrator",
		strconv.FormatInt(id, 10), map[string]any{"source": "public_bootstrap", "username": username,
			"payment_code": modes.PaymentCode, "self_service": modes.SelfService}); err != nil {
		return adminauth.Session{}, err
	}
	// The creator continues straight into administration, so their first session
	// is part of the same commit as the account it belongs to.
	session, err := adminauth.IssueSessionTx(ctx, tx, id, username)
	if err != nil {
		return adminauth.Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return adminauth.Session{}, err
	}
	return session, nil
}
