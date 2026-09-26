package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/EziosWJ/canteen-wallet/backend/internal/employees"
)

func adminEmployeeRoutes(admin *http.ServeMux, service *employees.Service) {
	admin.HandleFunc("/api/admin/employees", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			items, err := service.List(r.Context())
			if err != nil {
				employeeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"employees": items})
		case http.MethodPost:
			var input employees.CreateInput
			if !decodeJSON(w, r, 16384, &input) {
				return
			}
			item, password, err := service.Create(r.Context(), principalFromContext(r.Context()).ID, input)
			if err != nil {
				employeeError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"employee": item, "temporary_password": password})
		default:
			w.Header().Set("Allow", "GET, POST")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
		}
	})
	admin.HandleFunc("/api/admin/employees/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/admin/employees/")
		parts := strings.Split(path, "/")
		id, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || id < 1 {
			notFound(w, r)
			return
		}
		actorID := principalFromContext(r.Context()).ID
		if len(parts) == 1 && r.Method == http.MethodGet {
			item, err := service.Get(r.Context(), id)
			if err != nil {
				employeeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, item)
			return
		}
		if len(parts) == 2 && parts[1] == "status" && r.Method == http.MethodPatch {
			var request struct {
				Status string `json:"status"`
			}
			if !decodeJSON(w, r, 1024, &request) {
				return
			}
			item, err := service.SetStatus(r.Context(), actorID, id, request.Status)
			if err != nil {
				employeeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, item)
			return
		}
		if len(parts) == 2 && parts[1] == "reset-password" && r.Method == http.MethodPost {
			password, err := service.ResetPassword(r.Context(), actorID, id)
			if err != nil {
				employeeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"temporary_password": password})
			return
		}
		notFound(w, r)
	})
}

func employeeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, employees.ErrInvalidInput), errors.Is(err, employees.ErrInvalidCredentials):
		WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid employee request")
	case errors.Is(err, employees.ErrConflict):
		WriteError(w, http.StatusConflict, CodeConflict, "employee number or phone already exists")
	case errors.Is(err, employees.ErrNotFound):
		WriteError(w, http.StatusNotFound, CodeNotFound, "employee not found")
	case errors.Is(err, employees.ErrInvalidState):
		WriteError(w, http.StatusConflict, CodeInvalidState, "employee state does not allow this operation")
	default:
		WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service is not ready")
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, value any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid request body")
		return false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid request body")
		return false
	}
	return true
}
