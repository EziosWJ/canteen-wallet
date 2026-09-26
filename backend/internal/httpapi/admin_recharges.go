package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/EziosWJ/canteen-wallet/backend/internal/ledger"
	"github.com/EziosWJ/canteen-wallet/backend/internal/recharges"
)

func adminRechargeRoutes(admin *http.ServeMux, service *recharges.Service) {
	admin.HandleFunc("/api/admin/recharges", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		var input recharges.ConfirmInput
		if !decodeJSON(w, r, 4096, &input) {
			return
		}
		result, err := service.Confirm(r.Context(), principalFromContext(r.Context()).ID, input)
		if err != nil {
			rechargeError(w, err)
			return
		}
		status := http.StatusCreated
		if result.Replayed {
			status = http.StatusOK
		}
		writeJSON(w, status, map[string]any{
			"receipt": result.Receipt, "transaction": fundTransactionJSON(result.Transaction),
			"replayed": result.Replayed,
		})
	})
	admin.HandleFunc("/api/admin/recharges/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/admin/recharges/")
		parts := strings.Split(path, "/")
		if len(parts) != 2 || parts[1] != "reverse" {
			notFound(w, r)
			return
		}
		originalID, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || originalID < 1 {
			notFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		var input recharges.ReverseInput
		if !decodeJSON(w, r, 2048, &input) {
			return
		}
		result, err := service.Reverse(r.Context(), principalFromContext(r.Context()).ID, originalID, input)
		if err != nil {
			rechargeError(w, err)
			return
		}
		status := http.StatusCreated
		if result.Replayed {
			status = http.StatusOK
		}
		writeJSON(w, status, map[string]any{
			"recharge_transaction_id": result.RechargeTransactionID,
			"transaction":             fundTransactionJSON(result.Transaction), "replayed": result.Replayed,
		})
	})
}

func fundTransactionJSON(entry ledger.Entry) map[string]any {
	return map[string]any{
		"id": entry.ID, "transaction_no": entry.TransactionNo, "account_id": entry.AccountID,
		"type": entry.Kind, "amount_cents": entry.Amount, "before_balance_cents": entry.BeforeBalance,
		"after_balance_cents": entry.AfterBalance, "administrator_id": entry.AdministratorID,
		"business_type": entry.BusinessType, "business_id": entry.BusinessID,
		"related_transaction_id": entry.RelatedTransactionID, "created_at": entry.CreatedAt,
	}
}

func rechargeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, recharges.ErrInvalidInput), errors.Is(err, ledger.ErrInvalidRequest):
		WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid recharge request")
	case errors.Is(err, recharges.ErrEmployeeNotFound):
		WriteError(w, http.StatusNotFound, CodeNotFound, "employee not found")
	case errors.Is(err, recharges.ErrRechargeNotFound):
		WriteError(w, http.StatusNotFound, CodeNotFound, "recharge not found")
	case errors.Is(err, recharges.ErrReceiptConflict), errors.Is(err, ledger.ErrBusinessConflict):
		WriteError(w, http.StatusConflict, CodeReceiptConflict, "receipt already used")
	case errors.Is(err, recharges.ErrAlreadyReversed), errors.Is(err, ledger.ErrDuplicateReference):
		WriteError(w, http.StatusConflict, CodeAlreadyReversed, "recharge already reversed")
	case errors.Is(err, ledger.ErrIdempotencyConflict):
		WriteError(w, http.StatusConflict, CodeIdempotencyConflict, "idempotency key already used")
	case errors.Is(err, ledger.ErrInsufficientFunds):
		WriteError(w, http.StatusConflict, CodeInsufficientFunds, "insufficient balance for reversal")
	case errors.Is(err, ledger.ErrAccountUnavailable):
		WriteError(w, http.StatusConflict, CodeAccountUnavailable, "account is closed")
	default:
		WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service is not ready")
	}
}
