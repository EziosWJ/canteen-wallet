package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/EziosWJ/canteen-wallet/backend/internal/accounts"
	"github.com/EziosWJ/canteen-wallet/backend/internal/adminauth"
	"github.com/EziosWJ/canteen-wallet/backend/internal/backups"
	"github.com/EziosWJ/canteen-wallet/backend/internal/employees"
	"github.com/EziosWJ/canteen-wallet/backend/internal/ledger"
	"github.com/EziosWJ/canteen-wallet/backend/internal/meals"
	"github.com/EziosWJ/canteen-wallet/backend/internal/paymenttokens"
	"github.com/EziosWJ/canteen-wallet/backend/internal/recharges"
	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
	"github.com/EziosWJ/canteen-wallet/backend/internal/terminal"
	"github.com/EziosWJ/canteen-wallet/backend/internal/webassets"
)

type Code string

const (
	CodeNotFound               Code = "NOT_FOUND"
	CodeMethodNotAllowed       Code = "METHOD_NOT_ALLOWED"
	CodeServiceUnavailable     Code = "SERVICE_UNAVAILABLE"
	CodeInvalidRequest         Code = "INVALID_REQUEST"
	CodeInvalidCredentials     Code = "INVALID_CREDENTIALS"
	CodeUnauthorized           Code = "UNAUTHORIZED"
	CodeConflict               Code = "CONFLICT"
	CodeInvalidState           Code = "INVALID_STATE"
	CodePasswordChangeRequired Code = "PASSWORD_CHANGE_REQUIRED"
	CodeAccountUnavailable     Code = "ACCOUNT_UNAVAILABLE"
	CodeRefreshTooSoon         Code = "REFRESH_TOO_SOON"
	CodeReceiptConflict        Code = "RECEIPT_CONFLICT"
	CodeIdempotencyConflict    Code = "IDEMPOTENCY_CONFLICT"
	CodeAlreadyReversed        Code = "ALREADY_REVERSED"
	CodeInsufficientFunds      Code = "INSUFFICIENT_FUNDS"
	CodePayoutConflict         Code = "PAYOUT_CONFLICT"
)

type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

func Public(db *sql.DB, admins *adminauth.Service, employeeService *employees.Service,
	mealService *meals.Service, tokenService *paymenttokens.Service, rechargeService *recharges.Service, backupService *backups.Service) http.Handler {
	mux := http.NewServeMux()
	healthRoutes(mux, db)
	adminRoutes(mux, db, admins, employeeService, mealService, rechargeService, accounts.New(db), backupService)
	employeeRoutes(mux, employeeService, tokenService, ledger.New(db), mealService)
	mux.HandleFunc("/api", notFound)
	mux.HandleFunc("/api/", notFound)
	mux.Handle("/", webassets.Handler())
	return mux
}

// Internal is served on a separate listener. Terminal authentication and
// consumption routes will be added here, never to Public.
func Internal(db *sql.DB, terminalService *terminal.Service) http.Handler {
	mux := http.NewServeMux()
	healthRoutes(mux, db)
	terminalRoutes(mux, terminalService)
	mux.HandleFunc("/", notFound)
	return mux
}

func healthRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !allowRead(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if !allowRead(w, r) {
			return
		}
		if err := store.Ready(r.Context(), db); err != nil {
			WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service is not ready")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write([]byte(`{"status":"ready"}`))
	})
}

func allowRead(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
	return false
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	WriteError(w, http.StatusNotFound, CodeNotFound, "resource not found")
}

func WriteError(w http.ResponseWriter, status int, code Code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Error{Code: code, Message: message})
}
