CREATE TRIGGER validate_payout_receipt BEFORE INSERT ON payout_receipts
BEGIN
    SELECT RAISE(ABORT, 'payout receipt does not match withdrawal')
    WHERE NOT EXISTS (
        SELECT 1 FROM transactions t JOIN accounts a ON a.id = t.account_id
        WHERE t.id = NEW.withdrawal_transaction_id
          AND t.type = 'BALANCE_WITHDRAWAL'
          AND t.business_type = 'PAYOUT_RECEIPT'
          AND t.business_id = NEW.payout_ref
          AND t.amount = -NEW.amount_cents
          AND t.administrator_id = NEW.recorded_by
          AND a.employee_id = NEW.employee_id
    );
END;
