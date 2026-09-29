package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/adminauth"
	"github.com/EziosWJ/canteen-wallet/backend/internal/employees"
	"github.com/EziosWJ/canteen-wallet/backend/internal/ledger"
	"github.com/EziosWJ/canteen-wallet/backend/internal/meals"
	"github.com/EziosWJ/canteen-wallet/backend/internal/paymenttokens"
	"github.com/EziosWJ/canteen-wallet/backend/internal/terminal"
)

func employeeRoutes(public *http.ServeMux, service *employees.Service, tokenService *paymenttokens.Service,
	terminalService *terminal.Service, ledgerService *ledger.Service, mealService *meals.Service) {
	public.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		var request struct {
			Phone    string `json:"phone"`
			Password string `json:"password"`
		}
		if !decodeJSON(w, r, 8192, &request) {
			return
		}
		if request.Phone == "" || request.Password == "" || len(request.Phone) > 24 || len(request.Password) > 1024 {
			WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid request body")
			return
		}
		session, err := service.Login(r.Context(), request.Phone, request.Password)
		if errors.Is(err, employees.ErrInvalidCredentials) {
			WriteError(w, http.StatusUnauthorized, CodeInvalidCredentials, "invalid credentials")
			return
		}
		if err != nil {
			employeeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, employeeSessionResponse(session))
	})
	public.Handle("/api/auth/logout", requireEmployee(service, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		err := service.Logout(r.Context(), employeePrincipal(r.Context()))
		if errors.Is(err, employees.ErrUnauthenticated) {
			employeeUnauthorized(w)
			return
		}
		if err != nil {
			employeeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	me := http.NewServeMux()
	me.HandleFunc("/api/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		principal := employeePrincipal(r.Context())
		if principal.MustChangePassword {
			writeJSON(w, http.StatusOK, map[string]any{"id": principal.ID, "must_change_password": true})
			return
		}
		item, err := service.Get(r.Context(), principal.ID)
		if err != nil {
			employeeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})
	me.HandleFunc("/api/me/account", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		principal := employeePrincipal(r.Context())
		if principal.MustChangePassword {
			passwordChangeRequired(w)
			return
		}
		item, err := service.Get(r.Context(), principal.ID)
		if err != nil {
			employeeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"balance": item.Balance, "status": item.AccountStatus})
	})
	me.HandleFunc("/api/me/transactions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		principal := employeePrincipal(r.Context())
		if principal.MustChangePassword {
			passwordChangeRequired(w)
			return
		}
		cursor := int64(0)
		if raw := r.URL.Query().Get("cursor"); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || parsed < 1 {
				WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid transaction cursor")
				return
			}
			cursor = parsed
		}
		limit := 20
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 100 {
				WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "limit must be between 1 and 100")
				return
			}
			limit = parsed
		}
		entries, more, err := ledgerService.ListForEmployee(r.Context(), principal.ID, cursor, limit)
		if err != nil {
			WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service is not ready")
			return
		}
		items := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			items = append(items, map[string]any{
				"id": entry.ID, "transaction_no": entry.TransactionNo,
				"type": entry.Kind, "amount_cents": entry.Amount,
				"balance_after_cents":    entry.AfterBalance,
				"related_transaction_id": entry.RelatedTransactionID,
				"created_at":             entry.CreatedAt,
			})
		}
		var nextCursor any
		if more {
			nextCursor = strconv.FormatInt(entries[len(entries)-1].ID, 10)
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nextCursor})
	})
	me.HandleFunc("/api/me/meal-periods", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		if employeePrincipal(r.Context()).MustChangePassword {
			passwordChangeRequired(w)
			return
		}
		periods, err := mealService.List(r.Context())
		if err != nil {
			WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service is not ready")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"meal_periods": periods})
	})
	me.HandleFunc("/api/me/change-password", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		var request struct {
			OldPassword string `json:"old_password"`
			NewPassword string `json:"new_password"`
		}
		if !decodeJSON(w, r, 8192, &request) {
			return
		}
		if request.OldPassword == "" || request.NewPassword == "" || len(request.OldPassword) > 1024 || len(request.NewPassword) > 1024 || request.OldPassword == request.NewPassword {
			WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid password change request")
			return
		}
		session, err := service.ChangePassword(r.Context(), employeePrincipal(r.Context()), request.OldPassword, request.NewPassword)
		switch {
		case errors.Is(err, employees.ErrInvalidCredentials):
			WriteError(w, http.StatusUnauthorized, CodeInvalidCredentials, "invalid credentials")
		case errors.Is(err, employees.ErrUnauthenticated):
			employeeUnauthorized(w)
		case errors.Is(err, adminauth.ErrWeakPassword):
			WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "new password must be 12 to 1024 bytes")
		case err != nil:
			employeeError(w, err)
		default:
			writeJSON(w, http.StatusOK, employeeSessionResponse(session))
		}
	})
	me.HandleFunc("/api/me/payment-token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		var input struct {
			PresentationID string `json:"presentation_id"`
		}
		if r.ContentLength != 0 && !decodeJSON(w, r, 1024, &input) {
			return
		}
		token, err := tokenService.Issue(r.Context(), employeePrincipal(r.Context()), input.PresentationID)
		switch {
		case errors.Is(err, paymenttokens.ErrPasswordChangeRequired):
			passwordChangeRequired(w)
		case errors.Is(err, paymenttokens.ErrAccountUnavailable):
			WriteError(w, http.StatusForbidden, CodeAccountUnavailable, "account is unavailable for consumption")
		case errors.Is(err, paymenttokens.ErrSessionInvalid):
			employeeUnauthorized(w)
		case errors.Is(err, paymenttokens.ErrPresentationUnavailable):
			WriteError(w, http.StatusConflict, CodeInvalidState, "presentation unavailable")
		case errors.Is(err, paymenttokens.ErrRefreshTooSoon):
			seconds := max(1, int(time.Until(token.RefreshAfter).Seconds())+1)
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"code": CodeRefreshTooSoon, "message": "payment token refresh requested too soon",
				"refresh_after": token.RefreshAfter, "presentation_id": token.PresentationID,
			})
		case err != nil:
			WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service is not ready")
		default:
			writeJSON(w, http.StatusOK, token)
		}
	})
	me.HandleFunc("/api/me/payment-presentation", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		if employeePrincipal(r.Context()).MustChangePassword {
			passwordChangeRequired(w)
			return
		}
		status, err := terminalService.Presentation(r.Context(), employeePrincipal(r.Context()), r.URL.Query().Get("id"))
		if errors.Is(err, terminal.ErrNotFound) {
			WriteError(w, http.StatusNotFound, CodeNotFound, "presentation not found")
			return
		}
		if err != nil {
			WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service unavailable")
			return
		}
		writeJSON(w, http.StatusOK, status)
	})
	me.HandleFunc("/api/me/pending-consumptions/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		if employeePrincipal(r.Context()).MustChangePassword {
			passwordChangeRequired(w)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/me/pending-consumptions/")
		parts := strings.Split(path, "/")
		if len(parts) != 2 {
			notFound(w, r)
			return
		}
		var result terminal.Result
		var err error
		switch parts[1] {
		case "confirm":
			result, err = terminalService.ConfirmEmployee(r.Context(), employeePrincipal(r.Context()), parts[0])
		case "cancel":
			result, err = terminalService.CancelEmployee(r.Context(), employeePrincipal(r.Context()), parts[0])
		default:
			notFound(w, r)
			return
		}
		if errors.Is(err, terminal.ErrNotFound) {
			WriteError(w, http.StatusNotFound, CodeNotFound, "pending consumption not found")
			return
		}
		if errors.Is(err, terminal.ErrEmployeeSessionInvalid) {
			employeeUnauthorized(w)
			return
		}
		if errors.Is(err, terminal.ErrInvalidRequest) {
			WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid pending id")
			return
		}
		if err != nil {
			WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service unavailable")
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	me.HandleFunc("/api/me/self-service", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		if employeePrincipal(r.Context()).MustChangePassword {
			passwordChangeRequired(w)
			return
		}
		// Opening the page is an explicit server operation that cancels this
		// employee's own waiting scan requests before showing the preview.
		preview, err := terminalService.SelfServiceSession(r.Context(), employeePrincipal(r.Context()))
		if err != nil {
			writeSelfServiceError(w, err, "self-service preview")
			return
		}
		writeJSON(w, http.StatusOK, preview)
	})
	me.HandleFunc("/api/me/self-service/", func(w http.ResponseWriter, r *http.Request) {
		if employeePrincipal(r.Context()).MustChangePassword {
			passwordChangeRequired(w)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/me/self-service/")
		parts := strings.Split(path, "/")
		if len(parts) != 2 {
			notFound(w, r)
			return
		}
		switch parts[1] {
		case "confirm":
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", "POST")
				WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
				return
			}
			outcome, err := terminalService.ConfirmSelfService(r.Context(), employeePrincipal(r.Context()), parts[0])
			if err != nil {
				writeSelfServiceError(w, err, "self-service confirmation")
				return
			}
			writeJSON(w, http.StatusOK, outcome)
		case "result":
			// Reading the outcome never charges; it lets a refreshed success page
			// recover the same consumption from the server.
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", "GET")
				WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
				return
			}
			outcome, err := terminalService.SelfServiceStatus(r.Context(), employeePrincipal(r.Context()), parts[0])
			if err != nil {
				writeSelfServiceError(w, err, "self-service result")
				return
			}
			writeJSON(w, http.StatusOK, outcome)
		default:
			notFound(w, r)
		}
	})
	me.HandleFunc("/api/me/", notFound)
	public.Handle("/api/me", requireEmployee(service, me))
	public.Handle("/api/me/", requireEmployee(service, me))
}

func writeSelfServiceError(w http.ResponseWriter, err error, operation string) {
	switch {
	case errors.Is(err, terminal.ErrNotFound):
		WriteError(w, http.StatusNotFound, CodeNotFound, operation+" not found")
	case errors.Is(err, terminal.ErrEmployeeSessionInvalid):
		employeeUnauthorized(w)
	case errors.Is(err, terminal.ErrInvalidRequest):
		WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid self-service request")
	default:
		WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service unavailable")
	}
}

func employeeSessionResponse(session employees.Session) map[string]any {
	return map[string]any{
		"access_token":         session.Token,
		"token_type":           "Bearer",
		"expires_at":           session.ExpiresAt,
		"employee_id":          session.EmployeeID,
		"must_change_password": session.MustChangePassword,
	}
}

type employeeContextKey struct{}

func employeePrincipal(ctx context.Context) employees.Principal {
	principal, _ := ctx.Value(employeeContextKey{}).(employees.Principal)
	return principal
}

func requireEmployee(service *employees.Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			employeeUnauthorized(w)
			return
		}
		principal, err := service.Authenticate(r.Context(), parts[1])
		if errors.Is(err, employees.ErrUnauthenticated) {
			employeeUnauthorized(w)
			return
		}
		if err != nil {
			employeeError(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), employeeContextKey{}, principal)))
	})
}

func employeeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	WriteError(w, http.StatusUnauthorized, CodeUnauthorized, "employee session required")
}

func passwordChangeRequired(w http.ResponseWriter) {
	WriteError(w, http.StatusForbidden, CodePasswordChangeRequired, "change temporary password first")
}
