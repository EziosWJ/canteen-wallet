// Package settings stores the system-wide operating configuration. The only
// configuration today is which consumption entrances the canteen offers.
package settings

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
)

var (
	ErrInvalidInput = errors.New("at least one consumption entrance must stay enabled")
	ErrUnavailable  = errors.New("consumption modes are unavailable")
)

// Modes is the enabled state of the two employee consumption entrances. At
// least one of them is always enabled.
type Modes struct {
	PaymentCode bool `json:"payment_code"`
	SelfService bool `json:"self_service"`
}

// Valid reports whether at least one entrance stays enabled, which is the one
// invariant the system enforces on every write.
func (m Modes) Valid() bool { return m.PaymentCode || m.SelfService }

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func read(ctx context.Context, db queryRower) (Modes, error) {
	var paymentCode, selfService int
	err := db.QueryRowContext(ctx, `SELECT payment_code, self_service FROM consumption_modes WHERE id = 1`).
		Scan(&paymentCode, &selfService)
	if errors.Is(err, sql.ErrNoRows) {
		return Modes{}, ErrUnavailable
	}
	if err != nil {
		return Modes{}, err
	}
	return Modes{PaymentCode: paymentCode == 1, SelfService: selfService == 1}, nil
}

// Get returns the current modes.
func (s *Service) Get(ctx context.Context) (Modes, error) { return read(ctx, s.db) }

// ModesTx reads the modes inside the caller's transaction, so a service that
// charges an employee observes the same snapshot as the money movement.
func (s *Service) ModesTx(ctx context.Context, tx *sql.Tx) (Modes, error) { return read(ctx, tx) }

// Update replaces the modes, records an audit event and finishes the requests
// that the mode being turned off left pending. Disabling an entrance must not
// charge an employee whose scan or self-service confirmation was already in
// flight, so those requests are failed inside this same transaction before the
// new configuration becomes visible.
func (s *Service) Update(ctx context.Context, actorID int64, next Modes) (Modes, error) {
	if actorID < 1 || !next.Valid() {
		return Modes{}, ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Modes{}, err
	}
	defer tx.Rollback()
	current, err := read(ctx, tx)
	if err != nil {
		return Modes{}, err
	}
	if current == next {
		return current, tx.Commit()
	}
	now := timeNow()
	if _, err := tx.ExecContext(ctx, `UPDATE consumption_modes SET payment_code=?, self_service=?,
		updated_by=?, updated_at=? WHERE id=1`, boolToInt(next.PaymentCode), boolToInt(next.SelfService), actorID, now); err != nil {
		return Modes{}, err
	}
	if current.PaymentCode && !next.PaymentCode {
		if err := invalidatePendingScans(ctx, tx); err != nil {
			return Modes{}, err
		}
	}
	if current.SelfService && !next.SelfService {
		if err := invalidateSelfServiceIntents(ctx, tx, now); err != nil {
			return Modes{}, err
		}
	}
	if err := store.RecordAudit(ctx, tx, actorID, "CONSUMPTION_MODES_UPDATED", "system_settings", "consumption_modes",
		map[string]any{"payment_code": next.PaymentCode, "self_service": next.SelfService}); err != nil {
		return Modes{}, err
	}
	if err := tx.Commit(); err != nil {
		return Modes{}, err
	}
	return next, nil
}

// invalidatePendingScans finishes every waiting scan confirmation without a
// charge and tells the waiting presentation it ended, so the terminal stops
// waiting and the employee starts a fresh flow after the entrance returns.
//
// Only one payment-code entrance exists system-wide, so every pending
// consumption belongs to it and none needs a further predicate.
func invalidatePendingScans(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `UPDATE pending_consumptions SET state='FAILED', result_code='MODE_DISABLED'
		WHERE state='PENDING'`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE payment_presentations SET state='FAILED', result_code='MODE_DISABLED'
		WHERE state='ACTIVE' AND EXISTS (
			SELECT 1 FROM pending_consumptions p JOIN payment_tokens t ON t.id=p.token_id
			WHERE t.presentation_id=payment_presentations.id AND p.result_code='MODE_DISABLED')`)
	return err
}

// invalidateSelfServiceIntents fails the unconsumed intents so a later retry of
// an old confirmation cannot charge under the previous configuration.
func invalidateSelfServiceIntents(ctx context.Context, tx *sql.Tx, now string) error {
	_, err := tx.ExecContext(ctx, `UPDATE self_service_intents SET state='FAILED', result_code='MODE_DISABLED', updated_at=?
		WHERE state='PENDING'`, now)
	return err
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func timeNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// SetTx writes the modes inside the caller's transaction. Initialization uses it
// so the first administrator and the initial entrance choice land together and
// cannot be observed half-applied. It writes no audit event of its own; the
// caller records one describing the operation.
func (s *Service) SetTx(ctx context.Context, tx *sql.Tx, actorID int64, next Modes) error {
	if !next.Valid() {
		return ErrInvalidInput
	}
	var actor any
	if actorID > 0 {
		actor = actorID
	}
	_, err := tx.ExecContext(ctx, `UPDATE consumption_modes SET payment_code=?, self_service=?,
		updated_by=?, updated_at=? WHERE id=1`, boolToInt(next.PaymentCode), boolToInt(next.SelfService), actor, timeNow())
	return err
}
