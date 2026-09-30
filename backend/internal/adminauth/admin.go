package adminauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
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
	ErrInvalidUsername      = errors.New("username must be 3 to 64 letters, digits, dots, underscores or hyphens")
	ErrInvalidCredentials   = errors.New("invalid credentials")
	ErrUnauthenticated      = errors.New("unauthenticated")
	ErrUsernameTaken        = errors.New("administrator username is already taken")
	ErrSecondFactorNotBound = errors.New("administrator has no second factor binding")
	ErrSecondFactorPending  = errors.New("no pending second factor enrollment")
	ErrSecondFactorExpired  = errors.New("pending second factor enrollment has expired")
	usernamePattern         = regexp.MustCompile(`^[a-z0-9._-]{3,64}$`)
)

// NormalizeUsername trims and lower-cases a requested administrator username, so
// every entry point stores and compares the same spelling.
func NormalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// ValidUsername reports whether a normalized administrator username is
// acceptable. Only the character set and length are enforced here; uniqueness is
// decided inside the transaction that inserts the row.
func ValidUsername(username string) bool { return usernamePattern.MatchString(username) }

const sessionDuration = 12 * time.Hour
const failedLoginDelay = 500 * time.Millisecond

// enrollmentWindow bounds how long a generated but unverified authenticator
// secret stays usable, so an abandoned enrollment cannot be completed later.
const enrollmentWindow = 10 * time.Minute

// SecondFactor is the optional administrator second factor. Required reports
// whether this administrator must supply a proof in addition to the password;
// an unbound administrator logs in with the password alone.
type SecondFactor interface {
	Required(ctx context.Context, administratorID int64) (bool, error)
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

// SecurityState is the authenticator binding status shown in the admin UI. It
// deliberately carries no secret material.
type SecurityState struct {
	SecondFactorBound   bool       `json:"second_factor_bound"`
	EnrollmentPending   bool       `json:"enrollment_pending"`
	EnrollmentExpiresAt *time.Time `json:"enrollment_expires_at,omitempty"`
}

// Enrollment is the response to starting a binding: the URI an authenticator
// scans plus the same secret in base32 for manual entry. It must never be cached
// and must not be written to logs.
type Enrollment struct {
	OTPAuthURI  string    `json:"otpauth_uri"`
	Secret      string    `json:"secret"`
	ExpiresAt   time.Time `json:"expires_at"`
	Replacement bool      `json:"replacement"`
}

func New(db *sql.DB, factor SecondFactor) *Service {
	return &Service{db: db, factor: factor, loginGate: make(chan struct{}, 2)}
}

func (s *Service) CreateAdmin(ctx context.Context, username, password string) (int64, error) {
	username = NormalizeUsername(username)
	if !ValidUsername(username) {
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

// CreateAdministratorByActor is the admin-UI path for creating a further
// administrator. The new account has no second factor binding and can sign in
// with the initial password. The audit event records the acting administrator
// and never the initial password or its hash.
func (s *Service) CreateAdministratorByActor(ctx context.Context, actorID int64, username, password string) (int64, error) {
	username = NormalizeUsername(username)
	if actorID < 1 {
		return 0, ErrUnauthenticated
	}
	if !ValidUsername(username) {
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
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM administrators WHERE lower(username)=?`, username).Scan(&existing); err != nil {
		return 0, err
	}
	if existing > 0 {
		return 0, ErrUsernameTaken
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
	if err := store.RecordAudit(ctx, tx, actorID, "ADMIN_ACCOUNT_CREATED", "administrator",
		strconv.FormatInt(id, 10), map[string]any{"source": "admin_api", "created_username": username}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// AdministratorSummary is the account list the admin UI shows. Binding status is
// included so an administrator can see whether a colleague has adopted the
// optional second factor.
type AdministratorSummary struct {
	ID                int64  `json:"id"`
	Username          string `json:"username"`
	SecondFactorBound bool   `json:"second_factor_bound"`
	CreatedAt         string `json:"created_at"`
}

// List returns every administrator account.
func (s *Service) List(ctx context.Context) ([]AdministratorSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username,
		CASE WHEN totp_secret IS NULL THEN 0 ELSE 1 END, created_at
		FROM administrators ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AdministratorSummary, 0)
	for rows.Next() {
		var item AdministratorSummary
		var bound int
		if err := rows.Scan(&item.ID, &item.Username, &bound, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.SecondFactorBound = bound == 1
		items = append(items, item)
	}
	return items, rows.Err()
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
	return s.loginLocked(ctx, username, password, secondFactorProof)
}

// loginLocked runs after the login concurrency gate is held. Callers that
// already hold the gate (initialization) use it to avoid nesting the gate.
func (s *Service) loginLocked(ctx context.Context, username, password, secondFactorProof string) (Session, error) {
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
	// A bound administrator must supply a valid code; an unbound one logs in
	// with the password alone. A supplied code for an unbound account is not a
	// login factor and is ignored.
	if s.factor != nil {
		required, err := s.factor.Required(ctx, id)
		if err != nil {
			return Session{}, err
		}
		if required {
			if err := s.factor.Verify(ctx, id, secondFactorProof); err != nil {
				return Session{}, s.failLoginWithDelay(ctx, id, "second_factor_failed")
			}
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

// Security reports the authenticator binding state of one administrator.
func (s *Service) Security(ctx context.Context, administratorID int64) (SecurityState, error) {
	var bound int
	var pendingAt sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT CASE WHEN totp_secret IS NULL THEN 0 ELSE 1 END, totp_pending_created_at
		FROM administrators WHERE id=? AND status='active'`, administratorID).Scan(&bound, &pendingAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SecurityState{}, ErrUnauthenticated
	}
	if err != nil {
		return SecurityState{}, err
	}
	state := SecurityState{SecondFactorBound: bound == 1}
	if pendingAt.Valid {
		if created, err := time.Parse(time.RFC3339Nano, pendingAt.String); err == nil {
			expires := created.Add(enrollmentWindow)
			if expires.After(time.Now().UTC()) {
				state.EnrollmentPending = true
				state.EnrollmentExpiresAt = &expires
			}
		}
	}
	return state, nil
}

// StartEnrollment creates a pending authenticator secret and returns the URI an
// authenticator can scan. The pending secret is not a login factor: only a
// successful confirmation activates it. Binding an unbound administrator needs
// no extra credentials beyond the session; replacing an existing binding
// requires the current password and a valid current TOTP code.
func (s *Service) StartEnrollment(ctx context.Context, principal Principal, password, currentCode string) (Enrollment, error) {
	if principal.ID < 1 || len(password) > 1024 || len(currentCode) > 128 {
		return Enrollment{}, ErrInvalidCredentials
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Enrollment{}, err
	}
	defer tx.Rollback()
	var username, hash string
	var bound int
	var lastStep int64
	err = tx.QueryRowContext(ctx, `SELECT username,password_hash,
		CASE WHEN totp_secret IS NULL THEN 0 ELSE 1 END, totp_last_step
		FROM administrators WHERE id=? AND status='active'`, principal.ID).Scan(&username, &hash, &bound, &lastStep)
	if errors.Is(err, sql.ErrNoRows) {
		return Enrollment{}, ErrUnauthenticated
	}
	if err != nil {
		return Enrollment{}, err
	}
	if bound == 1 {
		if !VerifyPassword(password, hash) {
			return Enrollment{}, ErrInvalidCredentials
		}
		var currentSecret []byte
		if err := tx.QueryRowContext(ctx, `SELECT totp_secret FROM administrators WHERE id=?`, principal.ID).Scan(&currentSecret); err != nil {
			return Enrollment{}, err
		}
		matchedStep, ok := matchTOTP(currentSecret, currentCode, lastStep, time.Now().UTC())
		if !ok {
			return Enrollment{}, ErrInvalidCredentials
		}
		// A replacement consumes the code it was authorised with, exactly as a
		// login does, so one current code cannot start several replacements
		// within the same 30-second step.
		lastStep = matchedStep
	}
	secret, err := newTOTPSecret()
	if err != nil {
		return Enrollment{}, err
	}
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE administrators SET totp_pending_secret=?,totp_pending_created_at=?,totp_last_step=?,updated_at=?
		WHERE id=?`, secret, stamp, lastStep, stamp, principal.ID); err != nil {
		return Enrollment{}, err
	}
	if err := store.RecordAudit(ctx, tx, principal.ID, "ADMIN_TOTP_ENROLLMENT_STARTED", "administrator",
		strconv.FormatInt(principal.ID, 10), map[string]any{"replacement": bound == 1}); err != nil {
		return Enrollment{}, err
	}
	if err := tx.Commit(); err != nil {
		return Enrollment{}, err
	}
	return Enrollment{OTPAuthURI: enrolOTPURI(username, secret),
		Secret:      base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret),
		ExpiresAt:   now.Add(enrollmentWindow),
		Replacement: bound == 1}, nil
}

// ConfirmEnrollment activates a pending binding. Only a valid code from the
// pending secret turns it on; a wrong, abandoned or expired enrollment leaves
// the administrator's login behaviour unchanged. Success revokes every existing
// session, including the one that started the enrollment.
func (s *Service) ConfirmEnrollment(ctx context.Context, principal Principal, code string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var pending []byte
	var pendingAt sql.NullString
	var bound int
	err = tx.QueryRowContext(ctx, `SELECT totp_pending_secret,totp_pending_created_at,
		CASE WHEN totp_secret IS NULL THEN 0 ELSE 1 END FROM administrators WHERE id=? AND status='active'`,
		principal.ID).Scan(&pending, &pendingAt, &bound)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrUnauthenticated
	}
	if err != nil {
		return false, err
	}
	if len(pending) != 20 || !pendingAt.Valid {
		return false, ErrSecondFactorPending
	}
	created, err := time.Parse(time.RFC3339Nano, pendingAt.String)
	if err != nil {
		return false, ErrSecondFactorPending
	}
	now := time.Now().UTC()
	if !now.Before(created.Add(enrollmentWindow)) {
		return false, ErrSecondFactorExpired
	}
	matched, ok := matchTOTP(pending, code, -1, now)
	if !ok {
		return false, ErrInvalidCredentials
	}
	if _, err := tx.ExecContext(ctx, `UPDATE administrators SET totp_secret=?,totp_last_step=?,
		totp_pending_secret=NULL,totp_pending_created_at=NULL,updated_at=? WHERE id=?`,
		pending, matched, now.Format(time.RFC3339Nano), principal.ID); err != nil {
		return false, err
	}
	if _, err := revokeSessionsTx(ctx, tx, principal.ID, now); err != nil {
		return false, err
	}
	action := "ADMIN_TOTP_BOUND"
	if bound == 1 {
		action = "ADMIN_TOTP_CHANGED"
	}
	if err := store.RecordAudit(ctx, tx, principal.ID, action, "administrator",
		strconv.FormatInt(principal.ID, 10), map[string]any{"replacement": bound == 1}); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return bound == 1, nil
}

// RemoveSecondFactor unbinds the authenticator. It requires the current
// password and a valid TOTP code, revokes every session and is audited.
func (s *Service) RemoveSecondFactor(ctx context.Context, principal Principal, password, currentCode string) error {
	if principal.ID < 1 || len(password) > 1024 || len(currentCode) > 128 {
		return ErrInvalidCredentials
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var hash string
	var secret []byte
	var lastStep int64
	err = tx.QueryRowContext(ctx, `SELECT password_hash,totp_secret,totp_last_step
		FROM administrators WHERE id=? AND status='active'`, principal.ID).Scan(&hash, &secret, &lastStep)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUnauthenticated
	}
	if err != nil {
		return err
	}
	if len(secret) != 20 {
		return ErrSecondFactorNotBound
	}
	if !VerifyPassword(password, hash) {
		return ErrInvalidCredentials
	}
	if _, ok := matchTOTP(secret, currentCode, lastStep, time.Now().UTC()); !ok {
		return ErrInvalidCredentials
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE administrators SET totp_secret=NULL,totp_last_step=-1,
		totp_pending_secret=NULL,totp_pending_created_at=NULL,updated_at=? WHERE id=?`,
		now.Format(time.RFC3339Nano), principal.ID); err != nil {
		return err
	}
	if _, err := revokeSessionsTx(ctx, tx, principal.ID, now); err != nil {
		return err
	}
	if err := store.RecordAudit(ctx, tx, principal.ID, "ADMIN_TOTP_UNBOUND", "administrator",
		strconv.FormatInt(principal.ID, 10), nil); err != nil {
		return err
	}
	return tx.Commit()
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
