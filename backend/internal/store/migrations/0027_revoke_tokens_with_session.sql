CREATE TRIGGER revoke_tokens_with_session AFTER UPDATE OF revoked_at ON employee_sessions
WHEN OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL
BEGIN
    UPDATE payment_tokens SET state = 'REVOKED'
    WHERE session_id = NEW.id AND state = 'ACTIVE';
END;
