CREATE TRIGGER payout_receipts_no_update BEFORE UPDATE ON payout_receipts
BEGIN
    SELECT RAISE(ABORT, 'payout receipts are immutable');
END;
