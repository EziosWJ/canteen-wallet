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
	"strconv"
	"strings"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
)

type TOTP struct{ db *sql.DB }

func NewTOTP(db *sql.DB) *TOTP { return &TOTP{db: db} }

func (factor *TOTP) Verify(ctx context.Context, administratorID int64, proof string) error {
	if len(proof) != 6 {
		return ErrInvalidCredentials
	}
	for _, r := range proof {
		if r < '0' || r > '9' {
			return ErrInvalidCredentials
		}
	}
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
	if len(secret) != 20 {
		return ErrInvalidCredentials
	}
	step := time.Now().Unix() / 30
	matched := -1
	for candidate := step - 1; candidate <= step+1; candidate++ {
		if candidate > last && hmac.Equal([]byte(proof), []byte(totpCode(secret, candidate))) {
			matched = int(candidate)
			break
		}
	}
	if matched < 0 {
		return ErrInvalidCredentials
	}
	_, err = tx.ExecContext(ctx, `UPDATE administrators SET totp_last_step=? WHERE id=?`, matched, administratorID)
	if err != nil {
		return err
	}
	return tx.Commit()
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

// EnrollTOTP runs only from the server CLI. Re-enrollment invalidates active
// sessions and is recorded as a recovery operation in the append-only audit.
func EnrollTOTP(ctx context.Context, db *sql.DB, username string) (string, error) {
	secret := make([]byte, 20)
	if _, err := rand.Read(secret); err != nil {
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
	_, err = tx.ExecContext(ctx, `UPDATE administrators SET totp_secret=?,totp_last_step=-1,updated_at=? WHERE id=?`, secret, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `UPDATE admin_sessions SET revoked_at=? WHERE administrator_id=? AND revoked_at IS NULL`, time.Now().Unix(), id)
	if err != nil {
		return "", err
	}
	if err := store.RecordAudit(ctx, tx, id, "ADMIN_TOTP_ENROLLED", "administrator", strconv.FormatInt(id, 10), map[string]any{"source": "cli", "os_uid": os.Getuid()}); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
	uri := "otpauth://totp/" + url.PathEscape("Canteen Wallet:"+username) + "?secret=" + encoded + "&issuer=" + url.QueryEscape("Canteen Wallet") + "&algorithm=SHA1&digits=6&period=30"
	return uri, nil
}
