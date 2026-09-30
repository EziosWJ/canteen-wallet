package adminauth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
)

// TOTP is the administrator authenticator second factor. Binding is optional:
// only a secret stored in totp_secret is an active login factor, and the secret
// of an unfinished enrollment kept in totp_pending_secret never authenticates a
// login on its own.
type TOTP struct{ db *sql.DB }

func NewTOTP(db *sql.DB) *TOTP { return &TOTP{db: db} }

// Required reports whether this administrator has an active binding. An
// unbound administrator, or one with only a pending enrollment, is not required
// to present a code.
func (factor *TOTP) Required(ctx context.Context, administratorID int64) (bool, error) {
	var bound int
	err := factor.db.QueryRowContext(ctx, `SELECT CASE WHEN totp_secret IS NULL THEN 0 ELSE 1 END
		FROM administrators WHERE id=?`, administratorID).Scan(&bound)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrInvalidCredentials
	}
	if err != nil {
		return false, err
	}
	return bound == 1, nil
}

func (factor *TOTP) Verify(ctx context.Context, administratorID int64, proof string) error {
	tx, err := factor.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var secret []byte
	var last int64
	err = tx.QueryRowContext(ctx, `SELECT totp_secret,totp_last_step FROM administrators WHERE id=?`, administratorID).Scan(&secret, &last)
	if err != nil {
		return err
	}
	matched, ok := matchTOTP(secret, proof, last, time.Now().UTC())
	if !ok {
		return ErrInvalidCredentials
	}
	if _, err := tx.ExecContext(ctx, `UPDATE administrators SET totp_last_step=? WHERE id=?`, matched, administratorID); err != nil {
		return err
	}
	return tx.Commit()
}

// matchTOTP accepts a code from the current authenticator step or the two
// neighbouring steps, and never accepts a step already used by this account.
func matchTOTP(secret []byte, proof string, last int64, at time.Time) (int64, bool) {
	if len(secret) != 20 || len(proof) != 6 {
		return 0, false
	}
	for _, r := range proof {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	step := at.Unix() / 30
	for candidate := step - 1; candidate <= step+1; candidate++ {
		if candidate > last && hmac.Equal([]byte(proof), []byte(totpCode(secret, candidate))) {
			return candidate, true
		}
	}
	return 0, false
}

func totpCode(secret []byte, step int64) string {
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(message[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 0x0f
	value := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000)
}

func newTOTPSecret() ([]byte, error) {
	secret := make([]byte, 20)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	return secret, nil
}

// enrolOTPURI builds the otpauth URI the administrator's authenticator scans.
func enrolOTPURI(username string, secret []byte) string {
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
	return "otpauth://totp/" + url.PathEscape("Canteen Wallet:"+username) + "?secret=" + encoded +
		"&issuer=" + url.QueryEscape("Canteen Wallet") + "&algorithm=SHA1&digits=6&period=30"
}

// revokeSessionsTx ends every existing session of one administrator inside the
// caller's transaction, so a security change cannot leave an old session usable.
func revokeSessionsTx(ctx context.Context, tx *sql.Tx, administratorID int64, now time.Time) (int64, error) {
	result, err := tx.ExecContext(ctx, `UPDATE admin_sessions SET revoked_at=?
		WHERE administrator_id=? AND revoked_at IS NULL`, now.Unix(), administratorID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// EnrollTOTP runs only from the server CLI. Re-enrollment invalidates active
// sessions and is recorded as a recovery operation in the append-only audit.
func EnrollTOTP(ctx context.Context, db *sql.DB, username string) (string, error) {
	secret, err := newTOTPSecret()
	if err != nil {
		return "", err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	username = strings.ToLower(strings.TrimSpace(username))
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM administrators WHERE username=? AND status='active'`, username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalidCredentials
	}
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `UPDATE administrators SET totp_secret=?,totp_last_step=-1,
		totp_pending_secret=NULL,totp_pending_created_at=NULL,updated_at=? WHERE id=?`,
		secret, now.Format(time.RFC3339Nano), id)
	if err != nil {
		return "", err
	}
	if _, err := revokeSessionsTx(ctx, tx, id, now); err != nil {
		return "", err
	}
	if err := store.RecordAudit(ctx, tx, id, "ADMIN_TOTP_ENROLLED", "administrator", strconv.FormatInt(id, 10), map[string]any{"source": "cli", "os_uid": os.Getuid()}); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return enrolOTPURI(username, secret), nil
}

// RecoverTOTP is the documented lost-authenticator recovery command. It clears
// both the active and the pending secret, revokes every existing session and
// records an audit event, so the administrator can sign in with the password
// alone and bind an authenticator again from the admin UI. It deliberately does
// not require an old TOTP code, and it never touches another account.
func RecoverTOTP(ctx context.Context, db *sql.DB, username string) error {
	username = strings.ToLower(strings.TrimSpace(username))
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	var hadBinding int
	err = tx.QueryRowContext(ctx, `SELECT id,
		CASE WHEN totp_secret IS NULL AND totp_pending_secret IS NULL THEN 0 ELSE 1 END
		FROM administrators WHERE username=? AND status='active'`, username).Scan(&id, &hadBinding)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidCredentials
	}
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE administrators SET totp_secret=NULL,totp_last_step=-1,
		totp_pending_secret=NULL,totp_pending_created_at=NULL,updated_at=? WHERE id=?`,
		now.Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	revoked, err := revokeSessionsTx(ctx, tx, id, now)
	if err != nil {
		return err
	}
	osUsername := "unknown"
	if current, err := user.Current(); err == nil {
		osUsername = current.Username
	}
	if err := store.RecordAudit(ctx, tx, id, "ADMIN_TOTP_RECOVERED", "administrator", strconv.FormatInt(id, 10),
		map[string]any{"source": "cli", "os_uid": os.Getuid(), "os_username": osUsername,
			"revoked_sessions": revoked, "had_binding": hadBinding == 1}); err != nil {
		return err
	}
	return tx.Commit()
}
