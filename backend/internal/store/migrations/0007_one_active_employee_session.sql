CREATE UNIQUE INDEX one_active_employee_session ON employee_sessions(employee_id) WHERE revoked_at IS NULL;
