package vars

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	integrationtypes "github.com/wf-pro-dev/tailkit/types/integrations"
	"github.com/wf-pro-dev/tailkitd/internal/config"
)

func TestVarsEndpointsBasicUsage(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir(), zap.NewNop())
	handler := NewHandler(config.VarsConfig{
		Enabled: true,
		Scopes: []integrationtypes.VarScope{
			{Project: "app", Env: "dev", Allow: []string{"read", "write"}},
		},
	}, store, zap.NewNop())

	mux := http.NewServeMux()
	handler.Register(mux)

	t.Run("config and scopes", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/vars/config", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("config status = %d, want %d", rec.Code, http.StatusOK)
		}

		req = httptest.NewRequest(http.MethodGet, "/vars", nil)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("scopes status = %d, want %d", rec.Code, http.StatusOK)
		}

		var scopes []config.VarScope
		if err := json.Unmarshal(rec.Body.Bytes(), &scopes); err != nil {
			t.Fatalf("unmarshal scopes: %v", err)
		}
		if len(scopes) != 1 || scopes[0].Project != "app" || scopes[0].Env != "dev" {
			t.Fatalf("scopes = %#v, want app/dev", scopes)
		}
	})

	t.Run("put get render delete", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/vars/app/dev/API_KEY", strings.NewReader("secret-value\n"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("put status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
		}

		req = httptest.NewRequest(http.MethodGet, "/vars/app/dev/API_KEY", nil)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("get status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var got map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal key response: %v", err)
		}
		if got["key"] != "API_KEY" || got["value"] != "secret-value" {
			t.Fatalf("get response = %#v, want API_KEY=secret-value", got)
		}

		req = httptest.NewRequest(http.MethodGet, "/vars/app/dev?format=env", nil)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("env status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
		}
		if body := rec.Body.String(); !strings.Contains(body, "API_KEY='secret-value'") {
			t.Fatalf("env body = %q, want API_KEY entry", body)
		}

		req = httptest.NewRequest(http.MethodDelete, "/vars/app/dev/API_KEY", nil)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("delete key status = %d, want %d", rec.Code, http.StatusNoContent)
		}

		req = httptest.NewRequest(http.MethodDelete, "/vars/app/dev", nil)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("delete scope status = %d, want %d", rec.Code, http.StatusNoContent)
		}

		req = httptest.NewRequest(http.MethodGet, "/vars/app/dev", nil)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("list after delete status = %d, want %d", rec.Code, http.StatusOK)
		}

		var listed map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
			t.Fatalf("unmarshal list response: %v", err)
		}
		if len(listed) != 0 {
			t.Fatalf("list after delete = %#v, want empty map", listed)
		}
	})
}
