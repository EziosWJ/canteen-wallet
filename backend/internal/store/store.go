package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Open limits this single-writer service to one SQLite connection. DSN settings
// are applied whenever database/sql opens or replaces that connection.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	file, err := os.OpenFile(absPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("create database file: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close database file: %w", err)
	}

	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(absPath)}
	u.RawQuery = url.Values{
		"_busy_timeout": {"5000"},
		"_foreign_keys": {"on"},
		"_journal_mode": {"WAL"},
		"_synchronous":  {"FULL"},
		"_txlock":       {"immediate"},
	}.Encode()
	dsn := u.String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to SQLite: %w", err)
	}
	if err := verifyPragmas(ctx, conn); err != nil {
		conn.Close()
		db.Close()
		return nil, err
	}
	if err := conn.Close(); err != nil {
		db.Close()
		return nil, fmt.Errorf("release SQLite connection: %w", err)
	}
	if err := Migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	if err := verifyAccountBalances(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func verifyAccountBalances(ctx context.Context, db *sql.DB) error {
	var inconsistent int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts a
		LEFT JOIN transactions t ON t.id = a.last_transaction_id
		WHERE (a.last_transaction_id IS NULL AND a.balance != 0)
		   OR (a.last_transaction_id IS NOT NULL AND
		       (t.id IS NULL OR t.account_id != a.id OR t.after_balance != a.balance))`).Scan(&inconsistent)
	if err != nil {
		return fmt.Errorf("verify account balances: %w", err)
	}
	if inconsistent != 0 {
		return fmt.Errorf("found %d account balances without a matching last transaction", inconsistent)
	}
	return nil
}

func verifyPragmas(ctx context.Context, conn *sql.Conn) error {
	var mode string
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("read SQLite journal mode: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("SQLite refused WAL mode: %s", mode)
	}
	for _, setting := range []struct {
		name string
		want int
	}{
		{"foreign_keys", 1},
		{"busy_timeout", 5000},
		{"synchronous", 2},
	} {
		var got int
		if err := conn.QueryRowContext(ctx, "PRAGMA "+setting.name).Scan(&got); err != nil {
			return fmt.Errorf("read SQLite %s: %w", setting.name, err)
		}
		if got != setting.want {
			return fmt.Errorf("SQLite %s is %d, want %d", setting.name, got, setting.want)
		}
	}
	return nil
}

func Ready(ctx context.Context, db *sql.DB) error {
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return db.QueryRowContext(checkCtx, "SELECT 1").Scan(new(int))
}
