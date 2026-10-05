package httpapi

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/EziosWJ/canteen-wallet/backend/internal/terminal"
)

func adminTerminalRoutes(admin *http.ServeMux, db *sql.DB) {
	admin.HandleFunc("/api/admin/terminals", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			items, err := terminal.List(r.Context(), db)
			if err != nil {
				terminalManagementError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": items})
		case http.MethodPost:
			var input struct {
				Name string `json:"name"`
			}
			if !decodeJSON(w, r, 1024, &input) {
				return
			}
			item, err := terminal.Create(r.Context(), db, principalFromContext(r.Context()).ID, input.Name)
			if err != nil {
				terminalManagementError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, item)
		default:
			terminalManagementMethod(w, "GET, POST")
		}
	})
	admin.HandleFunc("/api/admin/terminals/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/api/admin/terminals/"), "/")
		if len(parts) > 2 || parts[0] == "" {
			notFound(w, r)
			return
		}
		id, err := url.PathUnescape(parts[0])
		if err != nil {
			notFound(w, r)
			return
		}
		if len(parts) == 1 {
			if r.Method != http.MethodPatch {
				terminalManagementMethod(w, "PATCH")
				return
			}
			var input struct {
				Name    *string `json:"name"`
				Enabled *bool   `json:"enabled"`
			}
			if !decodeJSON(w, r, 1024, &input) {
				return
			}
			if err := terminal.Update(r.Context(), db, principalFromContext(r.Context()).ID, id, input.Name, input.Enabled); err != nil {
				terminalManagementError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch parts[1] {
		case "credential":
			if r.Method != http.MethodGet {
				terminalManagementMethod(w, "GET")
				return
			}
			key, err := terminal.Credential(r.Context(), db, id)
			if err != nil {
				terminalManagementError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"key": key})
		case "reset-key":
			if r.Method != http.MethodPost {
				terminalManagementMethod(w, "POST")
				return
			}
			key, err := terminal.ResetCredential(r.Context(), db, principalFromContext(r.Context()).ID, id)
			if err != nil {
				terminalManagementError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"key": key})
		case "config":
			if r.Method != http.MethodGet {
				terminalManagementMethod(w, "GET")
				return
			}
			// The browser supplies its actual externally visible base address,
			// including HTTP/HTTPS and any proxy prefix; never guess from Host.
			base := r.URL.Query().Get("server_url")
			parsed, err := url.Parse(base)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(base, "\r\n") {
				WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "valid HTTP or HTTPS server_url required")
				return
			}
			key, err := terminal.Credential(r.Context(), db, id)
			if err != nil {
				terminalManagementError(w, err)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="terminal.env"`)
			fmt.Fprintf(w, "CANTEEN_SERVER_URL=%s\nCANTEEN_TERMINAL_KEY=%s\n# Set the scanner path on the agent machine.\nCANTEEN_SCANNER_DEVICE='/dev/input/eventX'\nCANTEEN_SCANNER_MODE='evdev'\n", shellValue(strings.TrimRight(base, "/")), shellValue(key))
		default:
			notFound(w, r)
		}
	})
}

func shellValue(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func terminalManagementMethod(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
}

func terminalManagementError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, terminal.ErrInvalidRequest):
		WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "终端名称不能为空，长度不能超过 100 字节。")
	case errors.Is(err, terminal.ErrNotFound):
		WriteError(w, http.StatusNotFound, CodeNotFound, "terminal not found")
	case errors.Is(err, terminal.ErrCredentialUnavailable):
		WriteError(w, http.StatusConflict, CodeInvalidState, "原 key 无法取回，请使用原凭据文件或重置 key。")
	default:
		unavailable(w)
	}
}
