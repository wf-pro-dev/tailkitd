package files

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	types "github.com/wf-pro-dev/tailkit/types"
	integrationtypes "github.com/wf-pro-dev/tailkit/types/integrations"
	"go.uber.org/zap"
)

func TestHandleConfigSharesOnlySharedPaths(t *testing.T) {
	t.Parallel()

	handler := NewHandler(integrationtypes.FilesConfig{
		Enabled: true,
		Paths: []integrationtypes.PathRule{
			{Dir: "/safe/", Allow: []string{"read"}, Share: true},
			{Dir: "/hidden/", Allow: []string{"write"}, Share: false},
		},
	}, nil, nil, zap.NewNop())

	req := httptest.NewRequest(http.MethodGet, "/files/config", nil)
	rec := httptest.NewRecorder()

	handler.handleConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got integrationtypes.FilesConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if !got.Enabled {
		t.Fatalf("Enabled = false, want true")
	}
	if len(got.Paths) != 1 {
		t.Fatalf("len(Paths) = %d, want 1", len(got.Paths))
	}
	if got.Paths[0].Dir != "/safe/" {
		t.Fatalf("shared dir = %q, want %q", got.Paths[0].Dir, "/safe/")
	}
}

func TestServeHTTPReadFileStat(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "example.txt")
	content := []byte("tailkitd test payload")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	handler := NewHandler(integrationtypes.FilesConfig{
		Enabled: true,
		Paths: []integrationtypes.PathRule{
			{Dir: root + string(os.PathSeparator), Allow: []string{"read"}},
		},
	}, nil, nil, zap.NewNop())

	req := httptest.NewRequest(http.MethodGet, "/files?path="+path+"&stat=true", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var got types.FileStat
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	sum := sha256.Sum256(content)
	wantSHA := hex.EncodeToString(sum[:])
	if got.SHA256 != wantSHA {
		t.Fatalf("SHA256 = %q, want %q", got.SHA256, wantSHA)
	}
	if got.Name != "example.txt" {
		t.Fatalf("Name = %q, want %q", got.Name, "example.txt")
	}
	if got.Size != int64(len(content)) {
		t.Fatalf("Size = %d, want %d", got.Size, len(content))
	}
	if got.IsDir {
		t.Fatalf("IsDir = true, want false")
	}
	if got.Mode == "" {
		t.Fatalf("Mode = empty, want populated mode string")
	}
}

func TestServeHTTPListDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "alpha.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatalf("write alpha.txt: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	handler := NewHandler(integrationtypes.FilesConfig{
		Enabled: true,
		Paths: []integrationtypes.PathRule{
			{Dir: root + string(os.PathSeparator), Allow: []string{"read"}},
		},
	}, nil, nil, zap.NewNop())

	req := httptest.NewRequest(http.MethodGet, "/files?dir="+root+string(os.PathSeparator), nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var got []types.DirEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(got))
	}

	names := map[string]bool{}
	for _, entry := range got {
		names[entry.Name] = true
	}
	if !names["alpha.txt"] || !names["nested"] {
		t.Fatalf("directory entries = %#v, want alpha.txt and nested", got)
	}
}

func TestResolveInboxPath(t *testing.T) {
	t.Parallel()

	toolDir := filepath.Join(string(os.PathSeparator), "tmp", "recv", "tool") + string(os.PathSeparator)

	t.Run("allows nested relative path", func(t *testing.T) {
		got, err := resolveInboxPath(toolDir, "nested/file.txt")
		if err != nil {
			t.Fatalf("resolveInboxPath returned error: %v", err)
		}

		want := filepath.Join(toolDir, "nested", "file.txt")
		if got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
	})

	t.Run("rejects absolute path", func(t *testing.T) {
		_, err := resolveInboxPath(toolDir, filepath.Join(string(os.PathSeparator), "etc", "passwd"))
		if err == nil {
			t.Fatal("resolveInboxPath returned nil error, want absolute path rejection")
		}
	})

	t.Run("rejects traversal", func(t *testing.T) {
		_, err := resolveInboxPath(toolDir, "../escape.txt")
		if err == nil {
			t.Fatal("resolveInboxPath returned nil error, want traversal rejection")
		}
	})
}
