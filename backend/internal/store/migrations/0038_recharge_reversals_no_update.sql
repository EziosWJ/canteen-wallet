CREATE TRIGGER recharge_reversals_no_update BEFORE UPDATE ON recharge_reversals
BEGIN
    SELECT RAISE(ABORT, 'recharge reversal cannot be changed');
END;
