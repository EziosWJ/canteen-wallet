CREATE TRIGGER validate_transaction_reference BEFORE INSERT ON transactions
BEGIN
    SELECT RAISE(ABORT, 'refund must reverse a consumption on the same account')
    WHERE NEW.type = 'REFUND' AND NOT EXISTS (
        SELECT 1 FROM transactions original
        WHERE original.id = NEW.related_transaction_id AND original.type = 'CONSUME'
          AND original.account_id = NEW.account_id AND NEW.amount = -original.amount
    );
    SELECT RAISE(ABORT, 'reversal must reverse a recharge on the same account')
    WHERE NEW.type = 'RECHARGE_REVERSAL' AND NOT EXISTS (
        SELECT 1 FROM transactions original
        WHERE original.id = NEW.related_transaction_id AND original.type = 'RECHARGE'
          AND original.account_id = NEW.account_id AND NEW.amount = -original.amount
    );
    SELECT RAISE(ABORT, 'withdrawal must match a refund on a closed account')
    WHERE NEW.type = 'BALANCE_WITHDRAWAL'
      AND (SELECT status FROM accounts WHERE id = NEW.account_id) = 'CLOSED'
      AND NOT EXISTS (
          SELECT 1 FROM transactions original
          WHERE original.id = NEW.related_transaction_id AND original.type = 'REFUND'
            AND original.account_id = NEW.account_id AND NEW.amount = -original.amount
      );
    SELECT RAISE(ABORT, 'unexpected transaction reference')
    WHERE NEW.related_transaction_id IS NOT NULL
      AND NEW.type NOT IN ('REFUND', 'RECHARGE_REVERSAL', 'BALANCE_WITHDRAWAL');
    SELECT RAISE(ABORT, 'administrator required for funds operation')
    WHERE NEW.type != 'CONSUME' AND NEW.administrator_id IS NULL;
END;
