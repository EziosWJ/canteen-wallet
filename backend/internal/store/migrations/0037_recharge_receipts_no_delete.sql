CREATE TRIGGER recharge_receipts_no_delete BEFORE DELETE ON recharge_receipts
BEGIN
    SELECT RAISE(ABORT, 'recharge receipt cannot be deleted');
END;
