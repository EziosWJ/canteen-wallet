CREATE TRIGGER payment_token_valid_session BEFORE INSERT ON payment_tokens
WHEN typeof(NEW.token_hash) != 'blob' OR length(NEW.token_hash) != 32
  OR NEW.state != 'ACTIVE' OR NEW.expires_at > NEW.issued_at + 60
  OR NOT EXISTS (
      SELECT 1 FROM employee_sessions s
      JOIN employees e ON e.id = s.employee_id
      JOIN accounts a ON a.employee_id = e.id
      WHERE s.id = NEW.session_id AND s.employee_id = NEW.employee_id
        AND s.revoked_at IS NULL AND s.expires_at > NEW.issued_at
        AND e.must_change_password = 0 AND e.status = 'ACTIVE' AND a.status = 'ACTIVE'
  )
BEGIN
    SELECT RAISE(ABORT, 'payment token requires an active eligible session');
END;
