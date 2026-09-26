CREATE TRIGGER guard_account_balance BEFORE UPDATE OF balance, last_transaction_id ON accounts
WHEN NEW.balance IS NOT OLD.balance OR NEW.last_transaction_id IS NOT OLD.last_transaction_id
BEGIN
    SELECT RAISE(ABORT, 'account balance requires a new transaction')
    WHERE NEW.last_transaction_id IS NULL OR NOT EXISTS (
        SELECT 1 FROM transactions t
        WHERE t.id = NEW.last_transaction_id
          AND t.account_id = OLD.id
          AND t.before_balance = OLD.balance
          AND t.after_balance = NEW.balance
          AND t.id > COALESCE(OLD.last_transaction_id, 0)
    );
END;
