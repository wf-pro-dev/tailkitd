package access

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestRegistryRolePrecedence(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "grants.toml"), []byte(`
[[grants]]
identity = "bob@example.com"
target = "*"
role = "superadmin"

[[grants]]
identity = "bob@example.com"
target = "nextcloud"
role = "admin"
`), 0o644); err != nil {
		t.Fatalf("write grants: %v", err)
	}

	reg, err := NewRegistry(context.Background(), dir, zap.NewNop())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.Close()

	role, ok := reg.RoleFor("bob@example.com", "nextcloud")
	if !ok || role != "admin" {
		t.Fatalf("RoleFor exact = (%q, %v), want (admin, true)", role, ok)
	}
	role, ok = reg.RoleFor("bob@example.com", "other")
	if !ok || role != "superadmin" {
		t.Fatalf("RoleFor wildcard = (%q, %v), want (superadmin, true)", role, ok)
	}
}

func TestRegistryReloads(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewRegistry(context.Background(), dir, zap.NewNop())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.Close()

	if err := os.WriteFile(filepath.Join(dir, "grants.toml"), []byte(`
[[grants]]
identity = "bob@example.com"
target = "nextcloud"
role = "admin"
`), 0o644); err != nil {
		t.Fatalf("write grants: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if reg.Allow("bob@example.com", "service.write", "nextcloud") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("registry did not reload new grant")
}
