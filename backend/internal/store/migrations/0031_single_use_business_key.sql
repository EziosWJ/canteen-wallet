CREATE UNIQUE INDEX single_use_business_key ON transactions(type, business_type, business_id)
WHERE (type = 'RECHARGE' AND business_type = 'RECEIPT')
   OR (type = 'BALANCE_WITHDRAWAL' AND business_type = 'PAYOUT_RECEIPT')
   OR (type = 'BALANCE_ADJUSTMENT' AND business_type = 'ADJUSTMENT_CASE')
   OR (type = 'CONSUME' AND business_type = 'MANUAL_SUPPLY');
