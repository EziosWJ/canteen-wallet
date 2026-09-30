package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/EziosWJ/canteen-wallet/backend/internal/adminauth"
)

// adminAdministratorRoutes lets any signed-in administrator add a further
// administrator and see the accounts that exist. There is deliberately no role
// or permission tier: every administrator is equal, as the spec requires.
func adminAdministratorRoutes(admin *http.ServeMux, admins *adminauth.Service) {
	admin.HandleFunc("/api/admin/administrators", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			items, err := admins.List(r.Context())
			if err != nil {
				WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service is not ready")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"administrators": items})
		case http.MethodPost:
			var request struct {
				Username string `json:"username"`
				Password string `json:"password"`
			}
			if !decodeJSON(w, r, 8192, &request) {
				return
			}
			if strings.TrimSpace(request.Username) == "" || len(request.Username) > 64 || request.Password == "" || len(request.Password) > 1024 {
				WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid request body")
				return
			}
			actor := principalFromContext(r.Context())
			id, err := admins.CreateAdministratorByActor(r.Context(), actor.ID, request.Username, request.Password)
			if err != nil {
				writeAdministratorError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{
				"administrator": map[string]any{
					"id": id, "username": strings.ToLower(strings.TrimSpace(request.Username)),
					"second_factor_bound": false,
				},
			})
		default:
			w.Header().Set("Allow", "GET, POST")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
	})
}

func writeAdministratorError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, adminauth.ErrInvalidUsername):
		WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid administrator username")
	case errors.Is(err, adminauth.ErrWeakPassword):
		WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "initial password must be 12 to 1024 bytes")
	case errors.Is(err, adminauth.ErrUsernameTaken):
		WriteError(w, http.StatusConflict, CodeConflict, "administrator username is already taken")
	case errors.Is(err, adminauth.ErrUnauthenticated):
		unauthorized(w)
	default:
		WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service is not ready")
	}
}
