// Acceptance coverage for the upgrade path: a database created before SPEC-004
// keeps its existing second factor binding and gains both consumption modes.
package acceptance

import (
	"context"
	"crypto/rand"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/adminauth"
	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
)

// downgradedDatabase rewrites a migrated database back to the pre-SPEC-004
// schema, so migrating it again is a genuine upgrade rather than a fresh
// install. Only schema state is removed; the columns and rows the two new
// migrations introduce are exactly what the upgrade must recreate.
func downgradedDatabase(t *testing.T) (*env, []byte) {
	t.Helper()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load time zone: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := store.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	e := &env{t: t, db: db, dbPath: dbPath, location: location, admins: adminauth.New(db, adminauth.NewTOTP(db))}

	// A bound administrator exists in the old database. Its secret must survive
	// the upgrade untouched, so it is stored before the new columns are removed.
	secret := make([]byte, 20)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("generate secret: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO administrators (username, password_hash, totp_secret, totp_last_step, created_at, updated_at)
		VALUES (?, ?, ?, -1, ?, ?)`, adminUsername, mustPasswordHash(t), secret, stamp(), stamp()); err != nil {
		t.Fatalf("seed legacy administrator: %v", err)
	}
	// A second legacy account that had never bound a factor. Before SPEC-004 it
	// could not sign in at all; after the upgrade the password alone must work.
	if _, err := db.ExecContext(t.Context(), `INSERT INTO administrators (username, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?)`, unboundAdminUsername, mustPasswordHash(t), stamp(), stamp()); err != nil {
		t.Fatalf("seed unbound legacy administrator: %v", err)
	}

	for _, column := range []string{"totp_pending_secret", "totp_pending_created_at"} {
		if _, err := db.ExecContext(t.Context(), `ALTER TABLE administrators DROP COLUMN `+column); err != nil {
			t.Fatalf("drop %s: %v", column, err)
		}
	}
	if _, err := db.ExecContext(t.Context(), `DROP TABLE consumption_modes`); err != nil {
		t.Fatalf("drop consumption modes: %v", err)
	}
	for _, name := range []string{
		"0064_admin_totp_pending_secret.sql",
		"0065_admin_totp_pending_created_at.sql",
		"0066_consumption_modes.sql",
		"0067_consumption_modes_default.sql",
	} {
		if _, err := db.ExecContext(t.Context(), `DELETE FROM schema_migrations WHERE name=?`, name); err != nil {
			t.Fatalf("forget migration %s: %v", name, err)
		}
	}
	return e, secret
}

// unboundAdminUsername is the legacy account that never bound a second factor.
const unboundAdminUsername = "legacy-unbound-admin"

func TestUpgradeKeepsExistingBindingAndEnablesBothEntrances(t *testing.T) {
	e, secret := downgradedDatabase(t)
	defer e.db.Close()
	// Migrating is what an operator's restart does; no other step may be needed.
	if err := store.Migrate(context.Background(), e.db); err != nil {
		t.Fatalf("migrate existing database: %v", err)
	}
	e.startServers()
	defer e.public.Close()
	defer e.internal.Close()

	// The pre-existing binding still authenticates, so the upgrade removed nothing.
	e.loginAdmin(adminUsername, adminPassword, totpCode(secret, time.Now()))
	// And it is still reported as bound.
	if state := e.security(e.lastAdminToken); !state.SecondFactorBound {
		t.Fatalf("upgrade lost the existing binding: %+v", state)
	}
	// Both entrances are open after the upgrade, preserving previous behaviour.
	e.expectModes(t, e.lastAdminToken, true, true)
}

// TestUpgradeLetsANeverBoundAdministratorSignInWithThePassword covers story 31:
// an account that predates the optional second factor and never bound one can
// sign in with just its password once the upgrade is applied.
func TestUpgradeLetsANeverBoundAdministratorSignInWithThePassword(t *testing.T) {
	e, _ := downgradedDatabase(t)
	defer e.db.Close()
	if err := store.Migrate(context.Background(), e.db); err != nil {
		t.Fatalf("migrate existing database: %v", err)
	}
	e.startServers()
	defer e.public.Close()
	defer e.internal.Close()

	// The password alone is enough, and the account reports itself as unbound.
	session := e.loginAdmin(unboundAdminUsername, adminPassword, "")
	if state := e.security(session); state.SecondFactorBound {
		t.Fatalf("a never-bound legacy account came back bound: %+v", state)
	}
	// A supplied code is not a factor for it, so a wrong one must not lock it out.
	e.loginAdminRaw(unboundAdminUsername, adminPassword, "000000").expect(t, http.StatusOK, nil)
}

func stamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// mustPasswordHash hashes the fixture administrator's password, since the
// downgraded database is written directly rather than through the API.
func mustPasswordHash(t *testing.T) string {
	t.Helper()
	hash, err := adminauth.HashPassword(adminPassword)
	if err != nil {
		t.Fatalf("hash fixture password: %v", err)
	}
	return hash
}
