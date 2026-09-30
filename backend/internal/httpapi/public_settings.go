package httpapi

import (
	"net/http"

	"github.com/EziosWJ/canteen-wallet/backend/internal/settings"
)

// publicSettingsRoutes exposes the enabled consumption entrances to the
// employee pages, which hide an entrance that is switched off. Hiding the entry
// is presentation only; every consumption endpoint re-checks the mode on the
// server, so a direct request to a disabled entrance is still refused.
func publicSettingsRoutes(public *http.ServeMux, settingsService *settings.Service) {
	public.HandleFunc("/api/consumption-modes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		modes, err := settingsService.Get(r.Context())
		if err != nil {
			WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "consumption modes are unavailable")
			return
		}
		writeJSON(w, http.StatusOK, modes)
	})
}
