CREATE UNIQUE INDEX normalized_payout_ref ON payout_receipts(trim(payout_ref));
