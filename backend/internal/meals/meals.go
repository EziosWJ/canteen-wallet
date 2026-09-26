package meals

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
)

var (
	ErrInvalidPeriod  = errors.New("invalid meal period")
	ErrOverlap        = errors.New("meal periods overlap")
	ErrNotFound       = errors.New("meal period not found")
	ErrNoActivePeriod = errors.New("no active meal period")
)

type Period struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	StartTime  string `json:"start_time"`
	EndTime    string `json:"end_time"`
	PriceCents int64  `json:"price_cents"`
	Enabled    bool   `json:"enabled"`
}

type Input struct {
	Name       string `json:"name"`
	StartTime  string `json:"start_time"`
	EndTime    string `json:"end_time"`
	PriceCents int64  `json:"price_cents"`
	Enabled    bool   `json:"enabled"`
}

type Service struct {
	db       *sql.DB
	location *time.Location
}

func New(db *sql.DB, location *time.Location) *Service {
	if location == nil {
		location = time.Local
	}
	return &Service{db: db, location: location}
}

func (s *Service) List(ctx context.Context) ([]Period, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT code, name, start_minute, end_minute, price_cents, enabled
		FROM meal_periods ORDER BY start_minute`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	periods := make([]Period, 0, 3)
	for rows.Next() {
		period, err := scanPeriod(rows)
		if err != nil {
			return nil, err
		}
		periods = append(periods, period)
	}
	return periods, rows.Err()
}

func (s *Service) Update(ctx context.Context, actorID int64, code string, input Input) (Period, error) {
	input.Name = strings.TrimSpace(input.Name)
	if actorID < 1 || !validCode(code) || len(input.Name) < 1 || len(input.Name) > 64 ||
		input.PriceCents < 0 || (input.Enabled && input.PriceCents == 0) {
		return Period{}, ErrInvalidPeriod
	}
	start, err := parseMinute(input.StartTime, false)
	if err != nil {
		return Period{}, ErrInvalidPeriod
	}
	end, err := parseMinute(input.EndTime, true)
	if err != nil || end <= start {
		return Period{}, ErrInvalidPeriod
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Period{}, err
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM meal_periods WHERE code = ?`, code).Scan(&id); errors.Is(err, sql.ErrNoRows) {
		return Period{}, ErrNotFound
	} else if err != nil {
		return Period{}, err
	}
	var overlap int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meal_periods
		WHERE id != ? AND ? < end_minute AND ? > start_minute`, id, start, end).Scan(&overlap)
	if err != nil {
		return Period{}, err
	}
	if overlap > 0 {
		return Period{}, ErrOverlap
	}
	_, err = tx.ExecContext(ctx, `UPDATE meal_periods SET name = ?, start_minute = ?, end_minute = ?,
		price_cents = ?, enabled = ?, updated_at = ? WHERE id = ?`, input.Name, start, end,
		input.PriceCents, input.Enabled, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return Period{}, err
	}
	if err := store.RecordAudit(ctx, tx, actorID, "MEAL_PERIOD_UPDATED", "meal_period", code,
		map[string]any{"price_cents": input.PriceCents, "start_time": input.StartTime,
			"end_time": input.EndTime, "enabled": input.Enabled}); err != nil {
		return Period{}, err
	}
	if err := tx.Commit(); err != nil {
		return Period{}, err
	}
	return Period{Code: code, Name: input.Name, StartTime: input.StartTime, EndTime: input.EndTime,
		PriceCents: input.PriceCents, Enabled: input.Enabled}, nil
}

// ActiveAtTx resolves the price under the caller's funds transaction so a
// concurrent configuration change cannot alter a completed transaction.
func (s *Service) ActiveAtTx(ctx context.Context, tx *sql.Tx, at time.Time) (Period, error) {
	return activeAt(ctx, tx, at.In(s.location))
}

func (s *Service) ActiveAt(ctx context.Context, at time.Time) (Period, error) {
	return activeAt(ctx, s.db, at.In(s.location))
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func activeAt(ctx context.Context, db queryRower, local time.Time) (Period, error) {
	minute := local.Hour()*60 + local.Minute()
	period, err := scanPeriod(db.QueryRowContext(ctx, `SELECT code, name, start_minute, end_minute, price_cents, enabled
		FROM meal_periods WHERE enabled = 1 AND start_minute <= ? AND end_minute > ?`, minute, minute))
	if errors.Is(err, sql.ErrNoRows) {
		return Period{}, ErrNoActivePeriod
	}
	return period, err
}

type scanner interface{ Scan(...any) error }

func scanPeriod(row scanner) (Period, error) {
	var period Period
	var start, end, enabled int
	err := row.Scan(&period.Code, &period.Name, &start, &end, &period.PriceCents, &enabled)
	if err != nil {
		return Period{}, err
	}
	period.StartTime = formatMinute(start)
	period.EndTime = formatMinute(end)
	period.Enabled = enabled == 1
	return period, nil
}

func parseMinute(value string, allowEndOfDay bool) (int, error) {
	if allowEndOfDay && value == "24:00" {
		return 1440, nil
	}
	if len(value) != 5 || value[2] != ':' {
		return 0, ErrInvalidPeriod
	}
	hour, err := strconv.Atoi(value[:2])
	if err != nil {
		return 0, err
	}
	minute, err := strconv.Atoi(value[3:])
	if err != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, ErrInvalidPeriod
	}
	return hour*60 + minute, nil
}

func formatMinute(value int) string { return fmt.Sprintf("%02d:%02d", value/60, value%60) }

func validCode(code string) bool { return code == "BREAKFAST" || code == "LUNCH" || code == "DINNER" }
