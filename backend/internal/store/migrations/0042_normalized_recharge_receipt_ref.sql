CREATE UNIQUE INDEX normalized_recharge_receipt_ref ON recharge_receipts(trim(receipt_ref));
