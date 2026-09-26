CREATE TRIGGER meal_no_overlap_insert BEFORE INSERT ON meal_periods
WHEN EXISTS (
    SELECT 1 FROM meal_periods existing
    WHERE NEW.start_minute < existing.end_minute AND NEW.end_minute > existing.start_minute
)
BEGIN
    SELECT RAISE(ABORT, 'meal periods cannot overlap');
END;
