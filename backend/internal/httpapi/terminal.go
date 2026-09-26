package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/EziosWJ/canteen-wallet/backend/internal/terminal"
)

func terminalRoutes(mux *http.ServeMux, service *terminal.Service) {
	mux.HandleFunc("/api/v1/terminal/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			WriteError(w, http.StatusUnauthorized, CodeUnauthorized, "terminal credential required")
			return
		}
		id, err := service.Authenticate(r.Context(), parts[1])
		if errors.Is(err, terminal.ErrUnauthorized) {
			WriteError(w, http.StatusUnauthorized, CodeUnauthorized, "terminal credential invalid")
			return
		}
		if err != nil {
			WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service unavailable")
			return
		}
		switch r.URL.Path {
		case "/api/v1/terminal/scan":
			if r.Method != http.MethodPost {
				methodPost(w)
				return
			}
			var input struct {
				Token string `json:"token"`
			}
			if !decodeJSON(w, r, 1024, &input) {
				return
			}
			result, err := service.Scan(r.Context(), id, input.Token)
			terminalResult(w, result, err)
		case "/api/v1/terminal/heartbeat":
			if r.Method != http.MethodPost {
				methodPost(w)
				return
			}
			var input struct {
				ScannerStatus string `json:"scanner_status"`
				VoiceStatus   string `json:"voice_status"`
			}
			if !decodeJSON(w, r, 1024, &input) {
				return
			}
			if err := service.Heartbeat(r.Context(), id, input.ScannerStatus, input.VoiceStatus); err != nil {
				terminalResult(w, terminal.Result{}, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		case "/api/v1/terminal/pending":
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", "GET")
				WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
				return
			}
			result, err := service.LatestPending(r.Context(), id)
			terminalResult(w, result, err)
		case "/api/v1/terminal/pending/recent":
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", "GET")
				WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
				return
			}
			items, err := service.RecentPending(r.Context(), id)
			if err != nil {
				WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service unavailable")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": items})
		case "/api/v1/terminal/events":
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", "GET")
				WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
				return
			}
			after, _ := strconv.ParseInt(r.URL.Query().Get("after_id"), 10, 64)
			items, err := service.Events(r.Context(), id, after)
			if err != nil {
				WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service unavailable")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": items})
		default:
			if strings.HasPrefix(r.URL.Path, "/api/v1/terminal/pending/") {
				if r.Method != http.MethodGet {
					w.Header().Set("Allow", "GET")
					WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
					return
				}
				result, err := service.PendingStatus(r.Context(), id, strings.TrimPrefix(r.URL.Path, "/api/v1/terminal/pending/"))
				terminalResult(w, result, err)
				return
			}
			notFound(w, r)
		}
	})
}

func methodPost(w http.ResponseWriter) {
	w.Header().Set("Allow", "POST")
	WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
}

func terminalResult(w http.ResponseWriter, result terminal.Result, err error) {
	if errors.Is(err, terminal.ErrInvalidRequest) {
		WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid terminal request")
		return
	}
	if errors.Is(err, terminal.ErrNotFound) {
		WriteError(w, http.StatusNotFound, CodeNotFound, "pending consumption not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "service unavailable")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
