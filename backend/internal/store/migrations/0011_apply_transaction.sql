CREATE TRIGGER apply_transaction AFTER INSERT ON transactions
BEGIN
    SELECT RAISE(ABORT, 'account is closed')
    WHERE (SELECT status FROM accounts WHERE id = NEW.account_id) = 'CLOSED'
      AND NEW.type NOT IN ('REFUND', 'BALANCE_WITHDRAWAL');
    SELECT RAISE(ABORT, 'account is not active for consumption')
    WHERE NEW.type = 'CONSUME'
      AND (SELECT status FROM accounts WHERE id = NEW.account_id) != 'ACTIVE';
    UPDATE accounts SET balance = NEW.after_balance,
        last_transaction_id = NEW.id, updated_at = NEW.created_at
    WHERE id = NEW.account_id AND balance = NEW.before_balance;
    SELECT RAISE(ABORT, 'account balance changed') WHERE changes() != 1;
END;
