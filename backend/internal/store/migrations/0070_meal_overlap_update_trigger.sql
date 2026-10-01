CREATE TRIGGER meal_no_overlap_update BEFORE UPDATE OF start_minute, end_minute ON meal_periods
WHEN NOT EXISTS (
    SELECT 1 FROM meal_periods_bulk_write_guard WHERE id = 1 AND active = 1
) AND EXISTS (
    SELECT 1 FROM meal_periods existing
    WHERE existing.id != OLD.id
      AND NEW.start_minute < existing.end_minute AND NEW.end_minute > existing.start_minute
)
BEGIN
    SELECT RAISE(ABORT, 'meal periods cannot overlap');
END;
