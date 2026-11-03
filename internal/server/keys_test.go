package server

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/livecfg"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

func TestDistributedKeyGatewayRoutes(t *testing.T) {
	s, err := httpauth.OpenKeys(filepath.Join(t.TempDir(), "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	info, secret, err := s.Create("client")
	if err != nil {
		t.Fatal(err)
	}
	live := livecfg.New(livecfg.Snapshot{APIKey: "admin"})
	h := NewHandler(Config{APIKey: "stale", Keys: s, Live: live, Pool: pool.New(""), Upstream: upstream.New()})
	call := func(method, path, key string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for _, key := range []string{secret, "admin"} {
		if code := call("GET", "/v1/models", key); code != 200 {
			t.Fatalf("models: %d", code)
		}
		for _, path := range []string{"/v1/chat/completions", "/v1/search/messages"} {
			if code := call("POST", path, key); code == 401 {
				t.Fatalf("valid key rejected on %s", path)
			}
		}
	}
	if call("GET", "/status", secret) != 401 || call("GET", "/status", "admin") != 200 {
		t.Fatal("status must remain administrator only")
	}
	for _, key := range []string{"", "wrong", "stale"} {
		if call("GET", "/v1/models", key) != 401 {
			t.Fatal("invalid key accepted")
		}
	}
	if err := s.SetEnabled(info.ID, false); err != nil {
		t.Fatal(err)
	}
	if call("GET", "/v1/models", secret) != 401 {
		t.Fatal("disabled key accepted")
	}
	if err := s.SetEnabled(info.ID, true); err != nil {
		t.Fatal(err)
	}
	live.Store(livecfg.Snapshot{APIKey: "new-admin"})
	if call("GET", "/v1/models", secret) != 200 || call("GET", "/v1/models", "admin") != 401 || call("GET", "/status", "new-admin") != 200 {
		t.Fatal("admin rotation broke key isolation")
	}
	if err := s.Delete(info.ID); err != nil {
		t.Fatal(err)
	}
	if call("GET", "/v1/models", secret) != 401 {
		t.Fatal("deleted key accepted")
	}
}
