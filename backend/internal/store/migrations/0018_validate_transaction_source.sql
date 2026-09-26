CREATE TRIGGER validate_transaction_source BEFORE INSERT ON transactions
BEGIN
    SELECT RAISE(ABORT, 'invalid transaction business source')
    WHERE NOT (
        (NEW.type = 'RECHARGE' AND NEW.business_type = 'RECEIPT') OR
        (NEW.type = 'RECHARGE_REVERSAL' AND NEW.business_type = 'RECHARGE') OR
        (NEW.type = 'CONSUME' AND NEW.terminal_id IS NOT NULL AND NEW.business_type = 'MEAL_PERIOD') OR
        (NEW.type = 'CONSUME' AND NEW.administrator_id IS NOT NULL AND NEW.business_type = 'MANUAL_SUPPLY') OR
        (NEW.type = 'REFUND' AND NEW.business_type = 'CONSUME') OR
        (NEW.type = 'BALANCE_ADJUSTMENT' AND NEW.business_type = 'ADJUSTMENT_CASE') OR
        (NEW.type = 'BALANCE_WITHDRAWAL' AND NEW.business_type = 'PAYOUT_RECEIPT')
    );
END;
