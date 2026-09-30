package httpapi

import (
	"errors"
	"net/http"

	"github.com/EziosWJ/canteen-wallet/backend/internal/bootstrap"
	"github.com/EziosWJ/canteen-wallet/backend/internal/settings"
)

// publicBootstrapRoutes host the first-administrator initialization entry.
//
// The entry exists only while no administrator exists. Once one does, the
// status endpoint reports it and the create endpoint refuses, so the normal
// administrator-creation flow cannot be bypassed.
func publicBootstrapRoutes(public *http.ServeMux, boot *bootstrap.Service) {
	public.HandleFunc("/api/bootstrap/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		required, err := boot.Required(r.Context())
		if err != nil {
			WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service is not ready")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"required": required})
	})

	public.HandleFunc("/api/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		var request struct {
			Username    string `json:"username"`
			Password    string `json:"password"`
			PaymentCode bool   `json:"payment_code"`
			SelfService bool   `json:"self_service"`
		}
		if !decodeJSON(w, r, 8192, &request) {
			return
		}
		if request.Username == "" || len(request.Username) > 64 || request.Password == "" || len(request.Password) > 1024 {
			WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid request body")
			return
		}
		// The account, its mode configuration and the creator's session commit
		// together, so a failure cannot leave an initialized system the creator
		// is unable to sign in to.
		session, err := boot.Initialize(r.Context(), request.Username, request.Password,
			settings.Modes{PaymentCode: request.PaymentCode, SelfService: request.SelfService})
		if err != nil {
			writeBootstrapError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"access_token":  session.Token,
			"token_type":    "Bearer",
			"expires_at":    session.ExpiresAt,
			"administrator": map[string]any{"id": session.Administrator.ID, "username": session.Administrator.Username},
			"next_step":     "configure_meal_periods",
		})
	})
}

func writeBootstrapError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, bootstrap.ErrAlreadyInitialized):
		WriteError(w, http.StatusConflict, CodeAlreadyInitialized, "the system already has an administrator")
	case errors.Is(err, bootstrap.ErrInvalidInput):
		WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid initialization request")
	default:
		WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service is not ready")
	}
}
