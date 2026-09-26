CREATE TRIGGER recharge_receipts_no_update BEFORE UPDATE ON recharge_receipts
BEGIN
    SELECT RAISE(ABORT, 'recharge receipt cannot be changed');
END;
