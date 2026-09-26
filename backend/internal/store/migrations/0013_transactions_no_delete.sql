CREATE TRIGGER transactions_no_delete BEFORE DELETE ON transactions
BEGIN
    SELECT RAISE(ABORT, 'fund transactions are immutable');
END;
