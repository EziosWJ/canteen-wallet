package httpapi

import (
	"net/http"

	"github.com/EziosWJ/canteen-wallet/backend/internal/backups"
)

func adminBackupRoutes(admin *http.ServeMux, service *backups.Service) {
	admin.HandleFunc("/api/admin/backups", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			items, err := service.List(r.Context())
			if err != nil {
				unavailable(w)
				return
			}
			writeJSON(w, 200, map[string]any{"items": items})
		case http.MethodPost:
			item, err := service.Create(r.Context(), principalFromContext(r.Context()).ID)
			if err != nil {
				WriteError(w, 503, CodeServiceUnavailable, err.Error())
				return
			}
			writeJSON(w, 201, map[string]any{"backup": item})
		default:
			methodPost(w)
		}
	})
}
