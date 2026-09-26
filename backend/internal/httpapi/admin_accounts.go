package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/EziosWJ/canteen-wallet/backend/internal/accounts"
	"github.com/EziosWJ/canteen-wallet/backend/internal/ledger"
)

func adminAccountRoutes(admin *http.ServeMux, service *accounts.Service) {
	admin.HandleFunc("/api/admin/accounts/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/admin/accounts/")
		parts := strings.Split(path, "/")
		if len(parts) != 2 || (parts[1] != "adjust" && parts[1] != "withdraw") {
			notFound(w, r)
			return
		}
		accountID, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || accountID < 1 {
			notFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		actorID := principalFromContext(r.Context()).ID
		if parts[1] == "adjust" {
			var input accounts.AdjustInput
			if !decodeJSON(w, r, 2048, &input) {
				return
			}
			result, err := service.Adjust(r.Context(), actorID, accountID, input)
			if err != nil {
				accountError(w, err)
				return
			}
			status := http.StatusCreated
			if result.Replayed {
				status = http.StatusOK
			}
			writeJSON(w, status, map[string]any{"transaction": fundTransactionJSON(result.Entry), "replayed": result.Replayed})
			return
		}
		var input accounts.WithdrawInput
		if !decodeJSON(w, r, 2048, &input) {
			return
		}
		result, err := service.Withdraw(r.Context(), actorID, accountID, input)
		if err != nil {
			accountError(w, err)
			return
		}
		status := http.StatusCreated
		if result.Replayed {
			status = http.StatusOK
		}
		writeJSON(w, status, map[string]any{
			"transaction": fundTransactionJSON(result.Transaction),
			"receipt":     result.Receipt, "replayed": result.Replayed,
		})
	})
}

func accountError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, accounts.ErrInvalidInput), errors.Is(err, ledger.ErrInvalidRequest):
		WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid account operation")
	case errors.Is(err, accounts.ErrNotFound), errors.Is(err, ledger.ErrAccountNotFound):
		WriteError(w, http.StatusNotFound, CodeNotFound, "account not found")
	case errors.Is(err, accounts.ErrInvalidState), errors.Is(err, ledger.ErrAccountUnavailable):
		WriteError(w, http.StatusConflict, CodeAccountUnavailable, "account state does not allow operation")
	case errors.Is(err, accounts.ErrPayoutConflict), errors.Is(err, ledger.ErrBusinessConflict):
		WriteError(w, http.StatusConflict, CodePayoutConflict, "payout reference already used")
	case errors.Is(err, ledger.ErrIdempotencyConflict):
		WriteError(w, http.StatusConflict, CodeIdempotencyConflict, "idempotency key already used")
	case errors.Is(err, ledger.ErrInsufficientFunds):
		WriteError(w, http.StatusConflict, CodeInsufficientFunds, "insufficient balance")
	case errors.Is(err, ledger.ErrDuplicateReference):
		WriteError(w, http.StatusConflict, CodeConflict, "refund already paid out")
	case errors.Is(err, ledger.ErrInvalidReference):
		WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid related refund")
	case errors.Is(err, ledger.ErrBalanceOverflow):
		WriteError(w, http.StatusConflict, CodeConflict, "balance limit exceeded")
	default:
		WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service is not ready")
	}
}
