-- Self-service consumption writes a CONSUME transaction that belongs to neither a
-- terminal nor an administrator, so the original source rules must be widened.
--
-- The transactions table is referenced by many child tables and triggers, so
-- ALTER TABLE ... RENAME rewrites (and re-validates) every dependent object and
-- fails. The source CHECK and the validate_transaction_source trigger are
-- rewritten in place instead: the trigger is replaced outright, and the table
-- CHECK text is edited through writable_schema.
PRAGMA writable_schema=ON;
DROP TRIGGER validate_transaction_source;
CREATE TRIGGER validate_transaction_source BEFORE INSERT ON transactions
BEGIN
    SELECT RAISE(ABORT, 'invalid transaction business source')
    WHERE NOT (
        (NEW.type = 'RECHARGE' AND NEW.business_type = 'RECEIPT') OR
        (NEW.type = 'RECHARGE_REVERSAL' AND NEW.business_type = 'RECHARGE') OR
        (NEW.type = 'CONSUME' AND NEW.terminal_id IS NOT NULL AND NEW.business_type = 'MEAL_PERIOD') OR
        (NEW.type = 'CONSUME' AND NEW.administrator_id IS NOT NULL AND NEW.business_type = 'MANUAL_SUPPLY') OR
        (NEW.type = 'CONSUME' AND NEW.terminal_id IS NULL AND NEW.administrator_id IS NULL AND NEW.business_type = 'SELF_SERVICE') OR
        (NEW.type = 'REFUND' AND NEW.business_type = 'CONSUME') OR
        (NEW.type = 'BALANCE_ADJUSTMENT' AND NEW.business_type = 'ADJUSTMENT_CASE') OR
        (NEW.type = 'BALANCE_WITHDRAWAL' AND NEW.business_type = 'PAYOUT_RECEIPT')
    );
END;
UPDATE sqlite_master SET sql = replace(sql,
    'CHECK ((administrator_id IS NOT NULL) != (terminal_id IS NOT NULL))',
    'CHECK ((administrator_id IS NOT NULL) != (terminal_id IS NOT NULL) OR (type = ''CONSUME'' AND business_type = ''SELF_SERVICE'' AND administrator_id IS NULL AND terminal_id IS NULL))')
    WHERE type = 'table' AND name = 'transactions';
PRAGMA writable_schema=OFF;
CREATE TEMP TABLE transaction_source_rewrite_guard(ok INTEGER NOT NULL CHECK (ok = 1));
INSERT INTO transaction_source_rewrite_guard(ok)
    SELECT CASE WHEN instr(sql, 'SELF_SERVICE') > 0 THEN 1 ELSE 0 END
    FROM sqlite_master WHERE name = 'transactions';
DROP TABLE transaction_source_rewrite_guard;
-- Editing sqlite_master does not by itself invalidate the prepared schema, so the
-- version every connection compares against must move past the previous value.
PRAGMA schema_version = 1000;
