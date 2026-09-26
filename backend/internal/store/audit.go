package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// RecordAudit writes an audit event inside the caller's transaction. Money
// operations should write their business rows and this event before committing.
// Callers must keep passwords, tokens and complete credentials out of details.
func RecordAudit(ctx context.Context, tx *sql.Tx, actorID int64, action, subjectType, subjectID string, details any) error {
	if action == "" {
		return fmt.Errorf("audit action is required")
	}
	encoded := []byte("{}")
	if details != nil {
		var err error
		encoded, err = json.Marshal(details)
		if err != nil {
			return fmt.Errorf("encode audit details: %w", err)
		}
	}
	var actor any
	if actorID > 0 {
		actor = actorID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_events
		(administrator_id, action, subject_type, subject_id, details_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, actor, action, nullIfEmpty(subjectType), nullIfEmpty(subjectID), string(encoded), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("record audit event: %w", err)
	}
	return nil
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
