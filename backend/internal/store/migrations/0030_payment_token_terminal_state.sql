CREATE TRIGGER payment_token_terminal_state BEFORE UPDATE OF state ON payment_tokens
WHEN OLD.state != 'ACTIVE' AND NEW.state != OLD.state
BEGIN
    SELECT RAISE(ABORT, 'payment token terminal state cannot change');
END;
