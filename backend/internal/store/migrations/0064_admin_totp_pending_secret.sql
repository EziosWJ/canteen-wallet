-- Optional administrator second factor: an administrator may exist without a
-- bound authenticator and log in with the password alone. An unfinished
-- enrollment is kept in its own column, separate from totp_secret, so a pending
-- secret is never accepted as a login factor.
ALTER TABLE administrators ADD COLUMN totp_pending_secret BLOB;
