package acceptance

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

type backupRecord struct {
	ID        int64  `json:"id"`
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	External  bool   `json:"external"`
}

func TestAdminBackupsReportSynchronousFailureAndCompletedExternalSnapshot(t *testing.T) {
	e := newEnv(t)
	var initial struct {
		Items []backupRecord `json:"items"`
	}
	e.get(e.public.URL, "/api/admin/backups", e.adminToken).expect(t, http.StatusOK, &initial)
	if len(initial.Items) != 0 {
		t.Fatalf("new fixture unexpectedly has backup history: %+v", initial.Items)
	}

	// With no configured external directory the synchronous request fails. The
	// API must not return a success-shaped response that a page could mistake for
	// a completed backup.
	e.post(e.public.URL, "/api/admin/backups", e.adminToken, map[string]string{}).
		expect(t, http.StatusServiceUnavailable, nil)

	e.backupExternalDir = filepath.Join(t.TempDir(), "external-backups")
	e.restartServers()
	var created struct {
		Backup backupRecord `json:"backup"`
	}
	e.post(e.public.URL, "/api/admin/backups", e.adminToken, map[string]string{}).
		expect(t, http.StatusCreated, &created)
	if created.Backup.ID < 1 || created.Backup.Status != "COMPLETE" || !created.Backup.External || created.Backup.Filename == "" || created.Backup.CreatedAt == "" {
		t.Fatalf("backup response did not confirm a completed external copy: %+v", created.Backup)
	}

	localSnapshot := filepath.Join(filepath.Dir(e.dbPath), "backups", created.Backup.Filename)
	externalSnapshot := filepath.Join(e.backupExternalDir, created.Backup.Filename)
	for _, path := range []string{localSnapshot, externalSnapshot} {
		info, err := os.Stat(path)
		if err != nil || info.Size() == 0 {
			t.Fatalf("completed backup snapshot %q is missing or empty: info=%v err=%v", path, info, err)
		}
	}

	var history struct {
		Items []backupRecord `json:"items"`
	}
	e.get(e.public.URL, "/api/admin/backups", e.adminToken).expect(t, http.StatusOK, &history)
	if len(history.Items) != 1 || history.Items[0].ID != created.Backup.ID || history.Items[0].Status != "COMPLETE" || !history.Items[0].External {
		t.Fatalf("backup history did not expose the completed result: %+v", history.Items)
	}
	e.get(e.public.URL, "/api/admin/backups", "").expect(t, http.StatusUnauthorized, nil)
	e.post(e.public.URL, "/api/admin/backups", "", map[string]string{}).expect(t, http.StatusUnauthorized, nil)
}
