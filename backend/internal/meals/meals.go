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

// PeriodInput identifies one fixed meal period as part of an atomic update.
type PeriodInput struct {
	Code       string `json:"code"`
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
	start, end, err := validateInput(input)
	if actorID < 1 || !validCode(code) || err != nil {
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

// UpdateAll validates and applies the three fixed meal periods as one final
// configuration. The database trigger remains active for ordinary writes; the
// transaction-local guard only postpones its row-by-row overlap check while
// these rows are updated, then the complete persisted set is checked before
// the guard, configuration, and audit record are committed together.
func (s *Service) UpdateAll(ctx context.Context, actorID int64, inputs []PeriodInput) ([]Period, error) {
	if actorID < 1 || len(inputs) != 3 {
		return nil, ErrInvalidPeriod
	}
	byCode := make(map[string]PeriodInput, len(inputs))
	minutes := make(map[string][2]int, len(inputs))
	for _, input := range inputs {
		input.Name = strings.TrimSpace(input.Name)
		if !validCode(input.Code) {
			return nil, ErrInvalidPeriod
		}
		if _, exists := byCode[input.Code]; exists {
			return nil, ErrInvalidPeriod
		}
		start, end, err := validateInput(Input{Name: input.Name, StartTime: input.StartTime,
			EndTime: input.EndTime, PriceCents: input.PriceCents, Enabled: input.Enabled})
		if err != nil {
			return nil, ErrInvalidPeriod
		}
		byCode[input.Code] = input
		minutes[input.Code] = [2]int{start, end}
	}
	for _, code := range []string{"BREAKFAST", "LUNCH", "DINNER"} {
		if _, exists := byCode[code]; !exists {
			return nil, ErrInvalidPeriod
		}
	}
	for i, left := range []string{"BREAKFAST", "LUNCH", "DINNER"} {
		for _, right := range []string{"BREAKFAST", "LUNCH", "DINNER"}[i+1:] {
			l, r := minutes[left], minutes[right]
			if l[0] < r[1] && l[1] > r[0] {
				return nil, ErrOverlap
			}
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO meal_periods_bulk_write_guard (id, active)
		VALUES (1, 1) ON CONFLICT(id) DO UPDATE SET active = 1`); err != nil {
		return nil, err
	}
	updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	for _, code := range []string{"BREAKFAST", "LUNCH", "DINNER"} {
		input := byCode[code]
		start, end := minutes[code][0], minutes[code][1]
		result, err := tx.ExecContext(ctx, `UPDATE meal_periods SET name = ?, start_minute = ?, end_minute = ?,
			price_cents = ?, enabled = ?, updated_at = ? WHERE code = ?`, input.Name, start, end,
			input.PriceCents, input.Enabled, updatedAt, code)
		if err != nil {
			return nil, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if rows != 1 {
			return nil, ErrInvalidPeriod
		}
	}
	var count, overlaps int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meal_periods`).Scan(&count); err != nil {
		return nil, err
	}
	if count != 3 {
		return nil, ErrInvalidPeriod
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meal_periods a JOIN meal_periods b ON a.id < b.id
		WHERE a.start_minute < b.end_minute AND a.end_minute > b.start_minute`).Scan(&overlaps); err != nil {
		return nil, err
	}
	if overlaps > 0 {
		return nil, ErrOverlap
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meal_periods_bulk_write_guard SET active = 0 WHERE id = 1`); err != nil {
		return nil, err
	}
	result := make([]Period, 0, 3)
	for _, code := range []string{"BREAKFAST", "LUNCH", "DINNER"} {
		input := byCode[code]
		result = append(result, Period{Code: code, Name: input.Name, StartTime: formatMinute(minutes[code][0]),
			EndTime: formatMinute(minutes[code][1]), PriceCents: input.PriceCents, Enabled: input.Enabled})
	}
	if err := store.RecordAudit(ctx, tx, actorID, "MEAL_PERIODS_UPDATED", "meal_periods", "all",
		map[string]any{"meal_periods": result}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func validateInput(input Input) (int, int, error) {
	if len(input.Name) < 1 || len(input.Name) > 64 || input.PriceCents < 0 ||
		(input.Enabled && input.PriceCents == 0) {
		return 0, 0, ErrInvalidPeriod
	}
	start, err := parseMinute(input.StartTime, false)
	if err != nil {
		return 0, 0, ErrInvalidPeriod
	}
	end, err := parseMinute(input.EndTime, true)
	if err != nil || end <= start {
		return 0, 0, ErrInvalidPeriod
	}
	return start, end, nil
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
