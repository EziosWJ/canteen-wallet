CREATE UNIQUE INDEX one_refund_or_reversal ON transactions(type, related_transaction_id)
WHERE type IN ('REFUND', 'RECHARGE_REVERSAL') AND related_transaction_id IS NOT NULL;
