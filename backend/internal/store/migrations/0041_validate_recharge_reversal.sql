CREATE TRIGGER validate_recharge_reversal BEFORE INSERT ON recharge_reversals
BEGIN
    SELECT RAISE(ABORT, 'reversal does not match recharge transaction')
    WHERE NOT EXISTS (
        SELECT 1 FROM transactions r
        JOIN transactions original ON original.id = NEW.recharge_transaction_id
        WHERE r.id = NEW.reversal_transaction_id
          AND r.type = 'RECHARGE_REVERSAL'
          AND original.type = 'RECHARGE'
          AND r.related_transaction_id = original.id
          AND r.account_id = original.account_id
          AND r.amount = -original.amount
          AND r.administrator_id = NEW.administrator_id
          AND r.reason = NEW.reason
    );
END;
