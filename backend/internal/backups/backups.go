package backups

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
)

type Service struct {
	DB                              *sql.DB
	DatabasePath, ExternalDirectory string
}

type Record struct {
	ID        int64  `json:"id"`
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	External  bool   `json:"external"`
}

func (s *Service) Create(ctx context.Context, actor int64) (Record, error) {
	if s.ExternalDirectory == "" {
		return Record{}, errors.New("external backup directory is required")
	}
	absolute, err := filepath.Abs(s.DatabasePath)
	if err != nil {
		return Record{}, err
	}
	localDir := filepath.Join(filepath.Dir(absolute), "backups")
	if err := os.MkdirAll(localDir, 0700); err != nil {
		return Record{}, err
	}
	if err := os.MkdirAll(s.ExternalDirectory, 0700); err != nil {
		return Record{}, err
	}
	now := time.Now().UTC()
	filename := "daily-" + now.Format("2006-01-02T150405.000000000Z") + ".db"
	localPath := filepath.Join(localDir, filename)
	// VACUUM INTO copies a consistent SQLite snapshot while WAL writers continue.
	if _, err := s.DB.ExecContext(ctx, `VACUUM INTO ?`, localPath); err != nil {
		return Record{}, err
	}
	if err := os.Chmod(localPath, 0600); err != nil {
		return Record{}, err
	}
	externalPath := filepath.Join(s.ExternalDirectory, filename)
	status := "COMPLETE"
	if err := copyFile(localPath, externalPath); err != nil {
		status = "EXTERNAL_FAILED"
		externalPath = ""
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	var external any
	if externalPath != "" {
		external = externalPath
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO backups(filename,local_path,external_path,status,created_by,created_at) VALUES(?,?,?,?,?,?)`, filename, localPath, external, status, nullActor(actor), now.Format(time.RFC3339Nano))
	if err != nil {
		return Record{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Record{}, err
	}
	if actor > 0 {
		if err := store.RecordAudit(ctx, tx, actor, "BACKUP_CREATED", "backup", fmt.Sprint(id), map[string]any{"status": status}); err != nil {
			return Record{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	if status == "COMPLETE" {
		monthName := "monthly-" + now.Format("2006-01") + ".db"
		monthPath := filepath.Join(s.ExternalDirectory, monthName)
		if _, err := os.Stat(monthPath); errors.Is(err, os.ErrNotExist) {
			if err := copyFile(localPath, monthPath); err != nil {
				_, _ = s.DB.ExecContext(ctx, `UPDATE backups SET status='EXTERNAL_FAILED' WHERE id=?`, id)
				return Record{ID: id, Filename: filename, Status: "EXTERNAL_FAILED", CreatedAt: now.Format(time.RFC3339Nano)}, err
			}
		}
		_ = prune(localDir, "daily-", 30)
		_ = prune(s.ExternalDirectory, "daily-", 30)
		_ = prune(s.ExternalDirectory, "monthly-", 12)
	}
	item := Record{ID: id, Filename: filename, Status: status, CreatedAt: now.Format(time.RFC3339Nano), External: externalPath != ""}
	if status != "COMPLETE" {
		return item, errors.New("external backup copy failed")
	}
	return item, nil
}

func (s *Service) List(ctx context.Context) ([]Record, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,filename,status,created_at,external_path IS NOT NULL FROM backups ORDER BY id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Record, 0)
	for rows.Next() {
		var item Record
		if err := rows.Scan(&item.ID, &item.Filename, &item.Status, &item.CreatedAt, &item.External); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) EnsureDaily(ctx context.Context) error {
	date := time.Now().UTC().Format("2006-01-02")
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM backups WHERE substr(created_at,1,10)=? AND status='COMPLETE'`, date).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err := s.Create(ctx, 0)
	return err
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := target + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, target)
}

func prune(directory, prefix string, keep int) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	names := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), ".db") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		if err := os.Remove(filepath.Join(directory, names[0])); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}

func nullActor(actor int64) any {
	if actor > 0 {
		return actor
	}
	return nil
}

// Restore is a CLI-only operation; stop the service before invoking it.
func Restore(ctx context.Context, source, target string) error {
	if source == "" || target == "" {
		return errors.New("source and target required")
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absTarget), 0700); err != nil {
		return err
	}
	tmp := absTarget + ".restore.tmp"
	if err := copyFile(source, tmp); err != nil {
		return err
	}
	defer os.Remove(tmp)
	check, err := sql.Open("sqlite", "file:"+tmp+"?mode=ro")
	if err != nil {
		return err
	}
	var integrity string
	err = check.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity)
	check.Close()
	if err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("backup integrity check: %s", integrity)
	}
	if _, err := os.Stat(absTarget); err == nil {
		return errors.New("target database exists; move it aside after stopping the service")
	}
	return os.Rename(tmp, absTarget)
}
