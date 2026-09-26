CREATE TRIGGER recharge_reversals_no_delete BEFORE DELETE ON recharge_reversals
BEGIN
    SELECT RAISE(ABORT, 'recharge reversal cannot be deleted');
END;
