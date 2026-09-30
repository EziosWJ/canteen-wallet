package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/EziosWJ/canteen-wallet/backend/internal/adminauth"
)

// adminSecurityRoutes exposes the administrator's own authenticator settings.
// Binding is optional: an unbound administrator signs in with the password
// alone and may start an enrollment here.
func adminSecurityRoutes(admin *http.ServeMux, admins *adminauth.Service) {
	// QR codes and secrets must never be cached by a browser or proxy.
	noStore := func(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }

	admin.HandleFunc("/api/admin/security", func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		state, err := admins.Security(r.Context(), principalFromContext(r.Context()).ID)
		if err != nil {
			writeAdminAuthError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, state)
	})

	admin.HandleFunc("/api/admin/security/enrollment", func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		var request struct {
			Password     string `json:"password"`
			SecondFactor string `json:"second_factor_code"`
		}
		if r.ContentLength != 0 && !decodeJSON(w, r, 2048, &request) {
			return
		}
		if len(request.Password) > 1024 || len(request.SecondFactor) > 128 {
			WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid request body")
			return
		}
		enrollment, err := admins.StartEnrollment(r.Context(), principalFromContext(r.Context()), request.Password, strings.TrimSpace(request.SecondFactor))
		if err != nil {
			writeAdminAuthError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, enrollment)
	})

	admin.HandleFunc("/api/admin/security/enrollment/confirm", func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		var request struct {
			SecondFactor string `json:"second_factor_code"`
		}
		if !decodeJSON(w, r, 2048, &request) {
			return
		}
		if request.SecondFactor == "" || len(request.SecondFactor) > 128 {
			WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid request body")
			return
		}
		replaced, err := admins.ConfirmEnrollment(r.Context(), principalFromContext(r.Context()), strings.TrimSpace(request.SecondFactor))
		if err != nil {
			writeAdminAuthError(w, err)
			return
		}
		// The successful change revoked every session of this administrator,
		// including the caller's, so the client must sign in again.
		writeJSON(w, http.StatusOK, map[string]any{"second_factor_bound": true, "replaced": replaced, "sessions_revoked": true})
	})
}

func writeAdminAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, adminauth.ErrInvalidCredentials):
		WriteError(w, http.StatusUnauthorized, CodeInvalidCredentials, "invalid password or second factor")
	case errors.Is(err, adminauth.ErrUnauthenticated):
		unauthorized(w)
	case errors.Is(err, adminauth.ErrSecondFactorPending):
		WriteError(w, http.StatusConflict, CodeInvalidState, "no pending second factor enrollment")
	case errors.Is(err, adminauth.ErrSecondFactorExpired):
		WriteError(w, http.StatusConflict, CodeInvalidState, "second factor enrollment has expired")
	default:
		WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service is not ready")
	}
}
