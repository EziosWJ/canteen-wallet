CREATE TRIGGER accounts_start_empty BEFORE INSERT ON accounts
WHEN NEW.balance != 0 OR NEW.last_transaction_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'new accounts must start with zero balance');
END;
