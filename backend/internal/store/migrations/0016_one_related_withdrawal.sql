CREATE UNIQUE INDEX one_related_withdrawal ON transactions(related_transaction_id)
WHERE type = 'BALANCE_WITHDRAWAL' AND related_transaction_id IS NOT NULL;
