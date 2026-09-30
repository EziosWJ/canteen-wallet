-- When the pending secret was issued, so an abandoned enrollment can be
-- recognised as expired and a fresh one can replace it.
ALTER TABLE administrators ADD COLUMN totp_pending_created_at TEXT;
