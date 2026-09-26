CREATE TRIGGER two_active_payment_tokens AFTER INSERT ON payment_tokens
BEGIN
    UPDATE payment_tokens SET state = 'REVOKED'
    WHERE session_id = NEW.session_id AND state = 'ACTIVE'
      AND id NOT IN (
          SELECT id FROM payment_tokens
          WHERE session_id = NEW.session_id AND state = 'ACTIVE'
          ORDER BY id DESC LIMIT 2
      );
END;
