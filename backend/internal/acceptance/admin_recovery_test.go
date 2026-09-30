// Acceptance coverage for CW-30 (#34): the lost-authenticator recovery command.
//
// The test builds the real server binary and runs its command-line entry point
// against the temporary database, then verifies the effect through the HTTP API.
// That keeps the assertion boundary at the operator-visible behaviour: the
// command clears the binding, revokes sessions and writes the audit event.
package acceptance

import (
	"encoding/base32"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// serverBinary builds the server once per test process. The binary lives in a
// process-wide temporary directory, not a per-test one, because a later test
// reuses it after the earlier test cleaned its own directory up.
var (
	serverBinaryOnce sync.Once
	serverBinaryPath string
	serverBinaryErr  error
)

func serverBinary(t *testing.T) string {
	t.Helper()
	serverBinaryOnce.Do(func() {
		directory, err := os.MkdirTemp("", "canteen-server-build-")
		if err != nil {
			serverBinaryErr = err
			return
		}
		serverBinaryPath = filepath.Join(directory, "canteen-server")
		build := exec.Command("go", "build", "-o", serverBinaryPath, "./cmd/server")
		build.Dir = backendModuleDir(t)
		if output, err := build.CombinedOutput(); err != nil {
			serverBinaryErr = fmt.Errorf("build server binary: %w\n%s", err, output)
		}
	})
	if serverBinaryErr != nil {
		t.Fatalf("%v", serverBinaryErr)
	}
	return serverBinaryPath
}

// backendModuleDir returns the backend module directory this test belongs to.
func backendModuleDir(t *testing.T) string {
	t.Helper()
	working, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for dir := working; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		if parent := filepath.Dir(dir); parent == dir {
			t.Fatalf("no go.mod found above %s", working)
		}
	}
}

// runRecoveryCommand runs the documented command against this test database.
func (e *env) runRecoveryCommand(t *testing.T, username string) ([]byte, error) {
	t.Helper()
	command := exec.Command(serverBinary(t), "recover-admin-totp", username)
	command.Env = append(os.Environ(), "CANTEEN_DB_PATH="+e.dbPath)
	return command.CombinedOutput()
}

// TestRecoveryCommandClearsBindingAndRevokesSessions covers the happy path: a
// bound administrator who lost the authenticator is recovered from the server
// without supplying any old TOTP code.
func TestRecoveryCommandClearsBindingAndRevokesSessions(t *testing.T) {
	e := newEnv(t)
	secret := e.enrollAdminCLI(adminUsername)
	session := e.loginAdmin(adminUsername, adminPassword, totpCode(secret, time.Now()))
	if output, err := e.runRecoveryCommand(t, adminUsername); err != nil {
		t.Fatalf("recovery command failed: %v\n%s", err, output)
	}
	e.get(e.public.URL, "/api/admin/me", session).expect(t, http.StatusUnauthorized, nil)
	e.loginAdmin(adminUsername, adminPassword, "")
	if state := e.security(e.lastAdminToken); state.SecondFactorBound {
		t.Fatalf("recovery did not clear the binding: %+v", state)
	}
	if !e.hasAuditAction(e.lastAdminToken, "ADMIN_TOTP_RECOVERED") {
		t.Fatal("recovery was not audited")
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
	for _, item := range e.auditEvents(e.lastAdminToken) {
		if item.Action != "ADMIN_TOTP_RECOVERED" {
			continue
		}
		lowered := strings.ToLower(item.Details)
		if strings.Contains(lowered, "otpauth") || strings.Contains(lowered, "secret") || strings.Contains(item.Details, encoded) {
			t.Fatalf("recovery audit leaked secret material: %s", item.Details)
		}
	}
}

// TestRecoveryCommandAlsoClearsAPendingEnrollment covers the requirement that a
// half-finished binding is cleared too, so it cannot be completed later.
func TestRecoveryCommandAlsoClearsAPendingEnrollment(t *testing.T) {
	e := newEnv(t)
	e.startEnrollment(e.adminToken)
	if output, err := e.runRecoveryCommand(t, adminUsername); err != nil {
		t.Fatalf("recovery command failed: %v\n%s", err, output)
	}
	e.loginAdmin(adminUsername, adminPassword, "")
	if state := e.security(e.lastAdminToken); state.EnrollmentPending || state.SecondFactorBound {
		t.Fatalf("recovery left enrollment state behind: %+v", state)
	}
}

// TestRecoveryCommandFailsForUnknownAdministrator covers the explicit failure
// and the guarantee that other accounts are untouched.
func TestRecoveryCommandFailsForUnknownAdministrator(t *testing.T) {
	e := newEnv(t)
	if output, err := e.runRecoveryCommand(t, "no-such-administrator"); err == nil {
		t.Fatalf("recovery of an unknown administrator should fail, got %s", output)
	}
	e.loginAdmin(adminUsername, adminPassword, "")
	if state := e.security(e.lastAdminToken); state.SecondFactorBound {
		t.Fatalf("failed recovery changed an unrelated account: %+v", state)
	}
}

// TestAdministratorCanRebindAfterRecovery covers the documented follow-up: the
// recovered administrator binds a new authenticator from the admin UI.
func TestAdministratorCanRebindAfterRecovery(t *testing.T) {
	e := newEnv(t)
	e.enrollAdminCLI(adminUsername)
	if output, err := e.runRecoveryCommand(t, adminUsername); err != nil {
		t.Fatalf("recovery command failed: %v\n%s", err, output)
	}
	e.loginAdmin(adminUsername, adminPassword, "")
	enrollment := e.startEnrollment(e.lastAdminToken)
	secret := e.pendingSecret(enrollment.Secret)
	e.post(e.public.URL, "/api/admin/security/enrollment/confirm", e.lastAdminToken,
		map[string]string{"second_factor_code": totpCode(secret, time.Now())}).expect(t, http.StatusOK, nil)
	e.loginAdmin(adminUsername, adminPassword, totpCode(secret, time.Now().Add(30*time.Second)))
}
