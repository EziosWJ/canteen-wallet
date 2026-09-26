CREATE TRIGGER revoke_tokens_with_account AFTER UPDATE OF status ON accounts
WHEN NEW.status != 'ACTIVE'
BEGIN
    UPDATE payment_tokens SET state = 'REVOKED'
    WHERE employee_id = NEW.employee_id AND state = 'ACTIVE';
END;
