CREATE TRIGGER accounts_no_delete BEFORE DELETE ON accounts
BEGIN
    SELECT RAISE(ABORT, 'accounts cannot be deleted');
END;
