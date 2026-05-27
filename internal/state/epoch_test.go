package state

import (
	"path/filepath"
	"testing"
)

func TestEpochPersistsAndValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.epoch")
	e, err := NewEpoch(path)
	if err != nil {
		t.Fatalf("NewEpoch: %v", err)
	}
	if e.Current() != 0 {
		t.Fatalf("Current = %d, want 0", e.Current())
	}
	if _, err := e.Increment(); err != nil {
		t.Fatalf("Increment: %v", err)
	}
	reloaded, err := NewEpoch(path)
	if err != nil {
		t.Fatalf("NewEpoch reload: %v", err)
	}
	if reloaded.Current() != 1 {
		t.Fatalf("Current reloaded = %d, want 1", reloaded.Current())
	}
	if err := reloaded.Validate(0); err == nil {
		t.Fatal("Validate stale caller returned nil")
	}
	if err := reloaded.Validate(1); err != nil {
		t.Fatalf("Validate matching caller returned error: %v", err)
	}
}
