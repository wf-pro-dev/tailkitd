package invite

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStorePersistsClaims(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claims.json")
	store, err := NewStore(path, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.MarkClaimed("token-1", "bob@example.com", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("MarkClaimed: %v", err)
	}

	reloaded, err := NewStore(path, nil)
	if err != nil {
		t.Fatalf("NewStore reload: %v", err)
	}
	if !reloaded.IsClaimed("token-1") {
		t.Fatal("reloaded store missing claimed token")
	}
}
