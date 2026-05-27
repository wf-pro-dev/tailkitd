package utils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteReplacesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("write original: %v", err)
	}

	if err := AtomicWrite(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("AtomicWrite returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "new" {
		t.Fatalf("file contents = %q, want %q", string(data), "new")
	}
}
