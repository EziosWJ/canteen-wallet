package terminal

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
)

var ErrCredentialUnavailable = errors.New("original terminal key unavailable; reset the key or use the existing credential file")

type Summary struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Enabled             bool   `json:"enabled"`
	CredentialAvailable bool   `json:"credential_available"`
	LastSeenAt          string `json:"last_seen_at"`
	ScannerStatus       string `json:"scanner_status"`
	VoiceStatus         string `json:"voice_status"`
	DatabaseStatus      string `json:"database_status"`
}

func validName(name string) bool {
	return strings.TrimSpace(name) != "" && len(name) <= 100
}

func List(ctx context.Context, db *sql.DB) ([]Summary, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,name,enabled,credential IS NOT NULL,COALESCE(last_seen_at,''),scanner_status,voice_status,database_status FROM terminals ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Summary, 0)
	for rows.Next() {
		var item Summary
		if err := rows.Scan(&item.ID, &item.Name, &item.Enabled, &item.CredentialAvailable, &item.LastSeenAt, &item.ScannerStatus, &item.VoiceStatus, &item.DatabaseStatus); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func Create(ctx context.Context, db *sql.DB, actor int64, name string) (Summary, error) {
	name = strings.TrimSpace(name)
	if !validName(name) {
		return Summary{}, ErrInvalidRequest
	}
	credential, hash, err := newCredential()
	if err != nil {
		return Summary{}, err
	}
	rawID := make([]byte, 12)
	if _, err := rand.Read(rawID); err != nil {
		return Summary{}, err
	}
	id := "terminal_" + hex.EncodeToString(rawID)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Summary{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO terminals(id,credential_hash,credential,name,created_at) VALUES (?,?,?,?,?)`, id, hash, credential, name, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return Summary{}, err
	}
	if err = store.RecordAudit(ctx, tx, actor, "TERMINAL_CREATED", "terminal", id, map[string]any{"name": name}); err != nil {
		return Summary{}, err
	}
	if err = tx.Commit(); err != nil {
		return Summary{}, err
	}
	return Summary{ID: id, Name: name, Enabled: true, CredentialAvailable: true, ScannerStatus: "UNKNOWN", VoiceStatus: "UNKNOWN", DatabaseStatus: "UNKNOWN"}, nil
}

func Credential(ctx context.Context, db *sql.DB, id string) (string, error) {
	var key sql.NullString
	err := db.QueryRowContext(ctx, `SELECT credential FROM terminals WHERE id=?`, id).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !key.Valid {
		return "", ErrCredentialUnavailable
	}
	return key.String, nil
}

// Update writes the change and its audit event together, preserving historical
// references and the credential when a terminal is renamed or disabled.
func Update(ctx context.Context, db *sql.DB, actor int64, id string, name *string, enabled *bool) error {
	if name == nil && enabled == nil {
		return ErrInvalidRequest
	}
	if name != nil {
		trimmed := strings.TrimSpace(*name)
		if !validName(trimmed) {
			return ErrInvalidRequest
		}
		name = &trimmed
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldName string
	var oldEnabled bool
	if err = tx.QueryRowContext(ctx, `SELECT name,enabled FROM terminals WHERE id=?`, id).Scan(&oldName, &oldEnabled); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	details := map[string]any{}
	if name != nil {
		oldName = *name
		details["name"] = *name
	}
	if enabled != nil {
		oldEnabled = *enabled
		details["enabled"] = *enabled
	}
	if _, err = tx.ExecContext(ctx, `UPDATE terminals SET name=?,enabled=? WHERE id=?`, oldName, oldEnabled, id); err != nil {
		return err
	}
	if err = store.RecordAudit(ctx, tx, actor, "TERMINAL_UPDATED", "terminal", id, details); err != nil {
		return err
	}
	return tx.Commit()
}

func ResetCredential(ctx context.Context, db *sql.DB, actor int64, id string) (string, error) {
	key, hash, err := newCredential()
	if err != nil {
		return "", err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE terminals SET credential=?,credential_hash=? WHERE id=?`, key, hash, id)
	if err != nil {
		return "", err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if count == 0 {
		return "", ErrNotFound
	}
	if err = store.RecordAudit(ctx, tx, actor, "TERMINAL_KEY_RESET", "terminal", id, nil); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return key, nil
}
