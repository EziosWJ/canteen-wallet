package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/EziosWJ/canteen-wallet/backend/internal/accounts"
	"github.com/EziosWJ/canteen-wallet/backend/internal/adminauth"
	"github.com/EziosWJ/canteen-wallet/backend/internal/backups"
	"github.com/EziosWJ/canteen-wallet/backend/internal/employees"
	"github.com/EziosWJ/canteen-wallet/backend/internal/meals"
	"github.com/EziosWJ/canteen-wallet/backend/internal/recharges"
)

func adminRoutes(public *http.ServeMux, db *sql.DB, admins *adminauth.Service, employeeService *employees.Service, mealService *meals.Service, rechargeService *recharges.Service, accountService *accounts.Service, backupService *backups.Service) {
	public.HandleFunc("/api/admin/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		var request struct {
			Username         string `json:"username"`
			Password         string `json:"password"`
			SecondFactorCode string `json:"second_factor_code"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid request body")
			return
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || request.Username == "" || request.Password == "" || len(request.Username) > 64 || len(request.Password) > 1024 || len(request.SecondFactorCode) > 128 {
			WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid request body")
			return
		}
		session, err := admins.Login(r.Context(), request.Username, request.Password, request.SecondFactorCode)
		if errors.Is(err, adminauth.ErrInvalidCredentials) {
			WriteError(w, http.StatusUnauthorized, CodeInvalidCredentials, "invalid credentials")
			return
		}
		if err != nil {
			WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service is not ready")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token":  session.Token,
			"token_type":    "Bearer",
			"expires_at":    session.ExpiresAt,
			"administrator": map[string]any{"id": session.Administrator.ID, "username": session.Administrator.Username},
		})
	})

	admin := http.NewServeMux()
	admin.HandleFunc("/api/admin/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		principal := principalFromContext(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"id": principal.ID, "username": principal.Username})
	})
	admin.HandleFunc("/api/admin/logout", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		if err := admins.Logout(r.Context(), principalFromContext(r.Context())); err != nil {
			if errors.Is(err, adminauth.ErrUnauthenticated) {
				unauthorized(w)
				return
			}
			WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service is not ready")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	adminEmployeeRoutes(admin, employeeService)
	adminMealRoutes(admin, mealService)
	adminRechargeRoutes(admin, rechargeService)
	adminAccountRoutes(admin, accountService)
	adminOperationRoutes(admin, db)
	adminBackupRoutes(admin, backupService)
	adminImportExportRoutes(admin, db, employeeService)
	admin.HandleFunc("/api/admin/", notFound)
	public.Handle("/api/admin/", requireAdmin(admins, admin))
}

type principalContextKey struct{}

func contextWithPrincipal(ctx context.Context, principal adminauth.Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

func principalFromContext(ctx context.Context) adminauth.Principal {
	principal, _ := ctx.Value(principalContextKey{}).(adminauth.Principal)
	return principal
}

func requireAdmin(admins *adminauth.Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			unauthorized(w)
			return
		}
		principal, err := admins.Authenticate(r.Context(), parts[1])
		if errors.Is(err, adminauth.ErrUnauthenticated) {
			unauthorized(w)
			return
		}
		if err != nil {
			WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service is not ready")
			return
		}
		next.ServeHTTP(w, r.WithContext(contextWithPrincipal(r.Context(), principal)))
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	WriteError(w, http.StatusUnauthorized, CodeUnauthorized, "administrator session required")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
