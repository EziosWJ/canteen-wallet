-- Existing databases migrate with both entrances enabled, which preserves the
-- behaviour of the previous release; a fresh database starts from the same row.
INSERT INTO consumption_modes (id, payment_code, self_service, updated_at)
VALUES (1, 1, 1, '2026-09-26T00:00:00Z');
