package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/EziosWJ/canteen-wallet/backend/internal/meals"
)

func adminMealRoutes(admin *http.ServeMux, service *meals.Service) {
	admin.HandleFunc("/api/admin/meal-periods", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			periods, err := service.List(r.Context())
			if err != nil {
				WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service is not ready")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"meal_periods": periods})
		case http.MethodPut:
			var input struct {
				MealPeriods []meals.PeriodInput `json:"meal_periods"`
			}
			if !decodeJSON(w, r, 8192, &input) {
				return
			}
			periods, err := service.UpdateAll(r.Context(), principalFromContext(r.Context()).ID, input.MealPeriods)
			switch {
			case errors.Is(err, meals.ErrInvalidPeriod):
				WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid meal periods")
			case errors.Is(err, meals.ErrOverlap):
				WriteError(w, http.StatusConflict, CodeConflict, "meal periods overlap")
			case err != nil:
				WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service is not ready")
			default:
				writeJSON(w, http.StatusOK, map[string]any{"meal_periods": periods})
			}
		default:
			w.Header().Set("Allow", "GET, PUT")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
		}
	})
	admin.HandleFunc("/api/admin/meal-periods/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.Header().Set("Allow", "PUT")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		code := strings.TrimPrefix(r.URL.Path, "/api/admin/meal-periods/")
		if code == "" || strings.Contains(code, "/") {
			notFound(w, r)
			return
		}
		var input meals.Input
		if !decodeJSON(w, r, 2048, &input) {
			return
		}
		period, err := service.Update(r.Context(), principalFromContext(r.Context()).ID, code, input)
		switch {
		case errors.Is(err, meals.ErrInvalidPeriod):
			WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid meal period")
		case errors.Is(err, meals.ErrOverlap):
			WriteError(w, http.StatusConflict, CodeConflict, "meal periods overlap")
		case errors.Is(err, meals.ErrNotFound):
			WriteError(w, http.StatusNotFound, CodeNotFound, "meal period not found")
		case err != nil:
			WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service is not ready")
		default:
			writeJSON(w, http.StatusOK, period)
		}
	})
}
