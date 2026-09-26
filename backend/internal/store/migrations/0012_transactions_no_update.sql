CREATE TRIGGER transactions_no_update BEFORE UPDATE ON transactions
BEGIN
    SELECT RAISE(ABORT, 'fund transactions are immutable');
END;
