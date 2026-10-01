package acceptance

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
)

type mealPeriod struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	StartTime  string `json:"start_time"`
	EndTime    string `json:"end_time"`
	PriceCents int64  `json:"price_cents"`
	Enabled    bool   `json:"enabled"`
}

func (e *env) mealPeriods(token string) []mealPeriod {
	e.t.Helper()
	var result struct {
		MealPeriods []mealPeriod `json:"meal_periods"`
	}
	e.get(e.public.URL, "/api/admin/meal-periods", token).expect(e.t, http.StatusOK, &result)
	return result.MealPeriods
}

func (e *env) updateMealPeriods(token string, periods []mealPeriod) apiResponse {
	e.t.Helper()
	return e.put(e.public.URL, "/api/admin/meal-periods", token, map[string]any{"meal_periods": periods})
}

func TestAdminCanAtomicallySwapMealPeriodsAndAuditTheFinalSet(t *testing.T) {
	e := newEnv(t)
	beforeAudits := e.countAuditAction(e.adminToken, "MEAL_PERIODS_UPDATED")
	updated := []mealPeriod{
		{Code: "BREAKFAST", Name: "早餐", StartTime: "11:00", EndTime: "14:00", Enabled: false},
		{Code: "LUNCH", Name: "午餐", StartTime: "06:00", EndTime: "09:00", Enabled: false},
		{Code: "DINNER", Name: "晚餐", StartTime: "17:00", EndTime: "20:00", Enabled: false},
	}
	var response struct {
		MealPeriods []mealPeriod `json:"meal_periods"`
	}
	e.updateMealPeriods(e.adminToken, updated).expect(t, http.StatusOK, &response)
	if len(response.MealPeriods) != 3 || response.MealPeriods[0].StartTime != "11:00" ||
		response.MealPeriods[1].StartTime != "06:00" {
		t.Fatalf("atomic boundary swap was not returned: %+v", response.MealPeriods)
	}
	if got := e.mealPeriods(e.adminToken); len(got) != 3 || got[0].Code != "LUNCH" || got[0].StartTime != "06:00" ||
		got[1].Code != "BREAKFAST" || got[1].StartTime != "11:00" {
		t.Fatalf("atomic boundary swap was not persisted: %+v", got)
	}
	if got := e.countAuditAction(e.adminToken, "MEAL_PERIODS_UPDATED"); got != beforeAudits+1 {
		t.Fatalf("bulk update wrote %d audit events, want one", got-beforeAudits)
	}
	for _, item := range e.auditEvents(e.adminToken) {
		if item.Action == "MEAL_PERIODS_UPDATED" {
			for _, code := range []string{"BREAKFAST", "LUNCH", "DINNER"} {
				if !strings.Contains(item.Details, code) {
					t.Fatalf("bulk audit is missing %s final state: %s", code, item.Details)
				}
			}
			return
		}
	}
	t.Fatal("bulk meal update audit event was not exposed")
}

func TestAdminMealBulkValidationRejectsWholeUpdate(t *testing.T) {
	e := newEnv(t)
	before := e.mealPeriods(e.adminToken)
	beforeAudits := e.countAuditAction(e.adminToken, "MEAL_PERIODS_UPDATED")
	invalid := []mealPeriod{
		{Code: "BREAKFAST", Name: "改过的早餐", StartTime: "05:00", EndTime: "08:00", PriceCents: 500, Enabled: true},
		{Code: "LUNCH", Name: "错误午餐", StartTime: "13:00", EndTime: "12:00", PriceCents: 800, Enabled: true},
		{Code: "DINNER", Name: "改过的晚餐", StartTime: "18:00", EndTime: "21:00", PriceCents: 900, Enabled: true},
	}
	e.updateMealPeriods(e.adminToken, invalid).expect(t, http.StatusBadRequest, nil)
	if got := e.mealPeriods(e.adminToken); !sameMealPeriods(got, before) {
		t.Fatalf("invalid meal period partially changed configuration: got %+v, want %+v", got, before)
	}
	if got := e.countAuditAction(e.adminToken, "MEAL_PERIODS_UPDATED"); got != beforeAudits {
		t.Fatalf("invalid update wrote %d audit events", got-beforeAudits)
	}
}

func TestAdminMealBulkAuditFailureRollsBackConfiguration(t *testing.T) {
	e := newEnv(t)
	before := e.mealPeriods(e.adminToken)
	beforeAudits := e.countAuditAction(e.adminToken, "MEAL_PERIODS_UPDATED")
	if _, err := e.db.ExecContext(t.Context(), `CREATE TRIGGER reject_meal_period_audit
		BEFORE INSERT ON audit_events WHEN NEW.action = 'MEAL_PERIODS_UPDATED'
		BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	updated := []mealPeriod{
		{Code: "BREAKFAST", Name: "早餐已修改", StartTime: "05:00", EndTime: "08:00", PriceCents: 500, Enabled: true},
		{Code: "LUNCH", Name: "午餐已修改", StartTime: "10:00", EndTime: "13:00", PriceCents: 800, Enabled: true},
		{Code: "DINNER", Name: "晚餐已修改", StartTime: "17:00", EndTime: "20:00", PriceCents: 900, Enabled: true},
	}
	e.updateMealPeriods(e.adminToken, updated).expect(t, http.StatusServiceUnavailable, nil)
	if got := e.mealPeriods(e.adminToken); !sameMealPeriods(got, before) {
		t.Fatalf("audit failure left a partial configuration: got %+v, want %+v", got, before)
	}
	if got := e.countAuditAction(e.adminToken, "MEAL_PERIODS_UPDATED"); got != beforeAudits {
		t.Fatalf("failed bulk update wrote %d audit events", got-beforeAudits)
	}
}

func TestAdminMealBulkEnforcesOverlapForDisabledPeriods(t *testing.T) {
	e := newEnv(t)
	periods := []mealPeriod{
		{Code: "BREAKFAST", Name: "早餐", StartTime: "08:00", EndTime: "10:00", Enabled: false},
		{Code: "LUNCH", Name: "午餐", StartTime: "09:30", EndTime: "12:00", Enabled: false},
		{Code: "DINNER", Name: "晚餐", StartTime: "17:00", EndTime: "20:00", Enabled: false},
	}
	e.updateMealPeriods(e.adminToken, periods).expect(t, http.StatusConflict, nil)
}

func TestAdminMealBulkAcceptsAdjacentPeriodsAndEndOfDay(t *testing.T) {
	e := newEnv(t)
	periods := []mealPeriod{
		{Code: "BREAKFAST", Name: "早餐", StartTime: "06:00", EndTime: "09:00", PriceCents: 500, Enabled: true},
		{Code: "LUNCH", Name: "午餐", StartTime: "09:00", EndTime: "12:00", PriceCents: 800, Enabled: true},
		{Code: "DINNER", Name: "晚餐", StartTime: "20:00", EndTime: "24:00", PriceCents: 1000, Enabled: true},
	}
	var response struct {
		MealPeriods []mealPeriod `json:"meal_periods"`
	}
	e.updateMealPeriods(e.adminToken, periods).expect(t, http.StatusOK, &response)
	if got := e.mealPeriods(e.adminToken); got[1].StartTime != "09:00" || got[2].EndTime != "24:00" {
		t.Fatalf("adjacent periods or 24:00 did not persist: %+v", got)
	}
}

func TestAdminSingleMealUpdateStillWorksAndRejectsOverlap(t *testing.T) {
	e := newEnv(t)
	valid := map[string]any{"name": "早餐", "start_time": "05:30", "end_time": "08:30", "price_cents": 500, "enabled": true}
	e.put(e.public.URL, "/api/admin/meal-periods/BREAKFAST", e.adminToken, valid).expect(t, http.StatusOK, nil)
	invalid := map[string]any{"name": "早餐", "start_time": "11:30", "end_time": "12:30", "price_cents": 500, "enabled": true}
	e.put(e.public.URL, "/api/admin/meal-periods/BREAKFAST", e.adminToken, invalid).expect(t, http.StatusConflict, nil)
}

func TestMealBulkMigrationPreservesExistingConfigurationAndTrigger(t *testing.T) {
	e := newEnv(t)
	before := e.mealPeriods(e.adminToken)
	// Recreate the pre-bulk-write trigger/schema state, then run the same
	// migration path used during an operator restart.
	if _, err := e.db.ExecContext(t.Context(), `DROP TRIGGER meal_no_overlap_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.ExecContext(t.Context(), `CREATE TRIGGER meal_no_overlap_update BEFORE UPDATE OF start_minute, end_minute ON meal_periods
		WHEN EXISTS (SELECT 1 FROM meal_periods existing WHERE existing.id != OLD.id
		AND NEW.start_minute < existing.end_minute AND NEW.end_minute > existing.start_minute)
		BEGIN SELECT RAISE(ABORT, 'meal periods cannot overlap'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.ExecContext(t.Context(), `DROP TABLE meal_periods_bulk_write_guard`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0068_meal_bulk_write_guard.sql", "0069_drop_meal_overlap_update_trigger.sql", "0070_meal_overlap_update_trigger.sql"} {
		if _, err := e.db.ExecContext(t.Context(), `DELETE FROM schema_migrations WHERE name=?`, name); err != nil {
			t.Fatalf("forget migration %s: %v", name, err)
		}
	}
	if err := store.Migrate(context.Background(), e.db); err != nil {
		t.Fatalf("migrate legacy meal periods: %v", err)
	}
	e.restartServers()
	if got := e.mealPeriods(e.adminToken); !sameMealPeriods(got, before) {
		t.Fatalf("migration changed existing meal configuration: got %+v, want %+v", got, before)
	}
	if _, err := e.db.ExecContext(t.Context(), `UPDATE meal_periods SET start_minute=700, end_minute=800 WHERE code='BREAKFAST'`); err == nil {
		t.Fatal("migrated overlap trigger allowed an ordinary write to overlap lunch")
	}
}

func sameMealPeriods(left, right []mealPeriod) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
