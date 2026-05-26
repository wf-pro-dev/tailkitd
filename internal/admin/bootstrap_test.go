package admin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type fakePeerAdminClient struct {
	answers map[string]bool
}

func (f fakePeerAdminClient) IsPeerAdmin(_ context.Context, hostname string) (bool, error) {
	return f.answers[hostname], nil
}

func TestDetermineIsAdminGenesis(t *testing.T) {
	t.Parallel()

	if !DetermineIsAdmin(context.Background(), "tailkitd-a", nil, fakePeerAdminClient{}) {
		t.Fatal("DetermineIsAdmin = false, want true for genesis node")
	}
}

func TestDetermineIsAdminDetectsExistingAdmin(t *testing.T) {
	t.Parallel()

	got := DetermineIsAdmin(context.Background(), "tailkitd-b", []string{"tailkitd-a"}, fakePeerAdminClient{
		answers: map[string]bool{"tailkitd-a": true},
	})
	if got {
		t.Fatal("DetermineIsAdmin = true, want false when another admin exists")
	}
}

func TestDetermineIsAdminUsesLexicalTieBreak(t *testing.T) {
	t.Parallel()

	got := DetermineIsAdmin(context.Background(), "tailkitd-b", []string{"tailkitd-a", "tailkitd-c"}, fakePeerAdminClient{})
	if got {
		t.Fatal("DetermineIsAdmin = true, want false when self loses lexical tie-break")
	}
}

func TestEnsureBootstrapFilesCreatesKeyAndFence(t *testing.T) {
	dir := t.TempDir()
	oldDir := AdminDirPath
	oldKey := AdminKeyPath
	oldFence := AdminFencePath
	AdminDirPath = dir
	AdminKeyPath = filepath.Join(dir, "admin.key")
	AdminFencePath = filepath.Join(dir, "admin.fence")
	defer func() {
		AdminDirPath = oldDir
		AdminKeyPath = oldKey
		AdminFencePath = oldFence
	}()

	if err := EnsureBootstrapFiles(); err != nil {
		t.Fatalf("EnsureBootstrapFiles returned error: %v", err)
	}

	keyData, err := os.ReadFile(AdminKeyPath)
	if err != nil {
		t.Fatalf("read admin key: %v", err)
	}
	if len(string(keyData)) != 32 {
		t.Fatalf("admin key length = %d, want 32", len(string(keyData)))
	}

	fenceData, err := os.ReadFile(AdminFencePath)
	if err != nil {
		t.Fatalf("read admin fence: %v", err)
	}
	if string(fenceData) != "0\n" {
		t.Fatalf("admin fence = %q, want %q", string(fenceData), "0\n")
	}
}
