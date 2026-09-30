package httpapi

import (
	"errors"
	"net/http"

	"github.com/EziosWJ/canteen-wallet/backend/internal/settings"
)

// adminSettingsRoutes reads and writes the canteen-wide consumption entrances.
// Every administrator may change them; the change is audited and takes effect
// for new consumption requests at commit time.
func adminSettingsRoutes(admin *http.ServeMux, settingsService *settings.Service) {
	admin.HandleFunc("/api/admin/settings/consumption-modes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPut {
			w.Header().Set("Allow", "GET, PUT")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		if r.Method == http.MethodGet {
			modes, err := settingsService.Get(r.Context())
			if err != nil {
				writeSettingsError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, modes)
			return
		}
		var input struct {
			PaymentCode bool `json:"payment_code"`
			SelfService bool `json:"self_service"`
		}
		if !decodeJSON(w, r, 2048, &input) {
			return
		}
		modes, err := settingsService.Update(r.Context(), principalFromContext(r.Context()).ID, settings.Modes{
			PaymentCode: input.PaymentCode, SelfService: input.SelfService,
		})
		if err != nil {
			writeSettingsError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, modes)
	})
}

func writeSettingsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, settings.ErrInvalidInput):
		WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "at least one consumption entrance must stay enabled")
	case errors.Is(err, settings.ErrUnavailable):
		WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "consumption modes are unavailable")
	default:
		WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service is not ready")
	}
}
