CREATE TRIGGER payout_receipts_no_delete BEFORE DELETE ON payout_receipts
BEGIN
    SELECT RAISE(ABORT, 'payout receipts cannot be deleted');
END;
