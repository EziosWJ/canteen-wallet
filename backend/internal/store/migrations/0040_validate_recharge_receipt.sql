CREATE TRIGGER validate_recharge_receipt BEFORE INSERT ON recharge_receipts
BEGIN
    SELECT RAISE(ABORT, 'receipt does not match recharge transaction')
    WHERE NOT EXISTS (
        SELECT 1 FROM transactions t
        JOIN accounts a ON a.id = t.account_id
        WHERE t.id = NEW.recharge_transaction_id
          AND t.type = 'RECHARGE'
          AND t.business_type = 'RECEIPT'
          AND t.business_id = NEW.receipt_ref
          AND t.amount = NEW.amount_cents
          AND t.administrator_id = NEW.recorded_by
          AND a.employee_id = NEW.employee_id
    );
END;
