CREATE TRIGGER payment_token_immutable_metadata BEFORE UPDATE OF token_hash, employee_id, session_id, issued_at, expires_at ON payment_tokens
BEGIN
    SELECT RAISE(ABORT, 'payment token metadata is immutable');
END;
