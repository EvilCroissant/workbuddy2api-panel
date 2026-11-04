package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

func TestKeyModelsEnforcedWithoutUsageRecorder(t *testing.T) {
	keys, err := httpauth.OpenKeys(filepath.Join(t.TempDir(), "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	info, secret, err := keys.CreateWithModels("restricted", []string{"cn:glm-5.2"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	up := newFakeUpstream(t, func(authz string) (int, string, bool) { calls++; return 200, sseOK, true })
	p := newRealmPool(t)
	p.SetCrossDomainFallback(true)
	h := NewHandler(Config{APIKey: "admin", Keys: keys, Pool: p, Upstream: up})
	post := func(key, model string, stream bool) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"model": model, "stream": stream, "messages": []map[string]string{{"role": "user", "content": "hello"}}})
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, stream := range []bool{false, true} {
		for _, model := range []string{"other", "cn:other", "global:glm-5.2", ""} {
			w := post(secret, model, stream)
			if w.Code != 403 || !strings.Contains(w.Body.String(), "model_not_allowed") {
				t.Fatalf("denied model=%q stream=%v: %d %s", model, stream, w.Code, w.Body.String())
			}
		}
	}
	if calls != 0 {
		t.Fatal("denied model reached upstream")
	}
	for _, model := range []string{"glm-5.2", "cn:glm-5.2"} {
		if w := post(secret, model, false); w.Code != 200 {
			t.Fatalf("allowed model: %d %s", w.Code, w.Body.String())
		}
	}
	if w := post("admin", "other", false); w.Code != 200 {
		t.Fatalf("admin restricted: %d %s", w.Code, w.Body.String())
	}
	// A CN-only policy must not fall back to a Global account, even for a bare ID.
	p.Disable("cn1", "test")
	calls = 0
	if w := post(secret, "glm-5.2", false); w.Code == 200 || calls != 0 {
		t.Fatalf("cross-realm permission bypass: %d calls=%d", w.Code, calls)
	}
	both := []string{"cn:glm-5.2", "global:glm-5.2"}
	if err := keys.Update(info.ID, nil, &both); err != nil {
		t.Fatal(err)
	}
	if w := post(secret, "glm-5.2", false); w.Code != 200 {
		t.Fatalf("authorized fallback failed: %d %s", w.Code, w.Body.String())
	}
}

func TestKeyModelListFiltersPerKey(t *testing.T) {
	dynamicModelsCache.Lock()
	oldIDs, oldFetched, oldFail := dynamicModelsCache.ids, dynamicModelsCache.fetched, dynamicModelsCache.lastFail
	dynamicModelsCache.ids = []upstream.ModelInfo{{ID: "glm-5.2"}, {ID: "deepseek-v3"}}
	dynamicModelsCache.fetched = time.Now()
	dynamicModelsCache.Unlock()
	t.Cleanup(func() {
		dynamicModelsCache.Lock()
		defer dynamicModelsCache.Unlock()
		dynamicModelsCache.ids, dynamicModelsCache.fetched, dynamicModelsCache.lastFail = oldIDs, oldFetched, oldFail
	})
	keys, err := httpauth.OpenKeys(filepath.Join(t.TempDir(), "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keys.CreateWithModels("restricted", []string{"cn:glm-5.2"})
	if err != nil {
		t.Fatal(err)
	}
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, `{}`, false })
	h := NewHandler(Config{APIKey: "admin", Keys: keys, Pool: newRealmPool(t), Upstream: up})
	for _, tc := range []struct {
		key   string
		count int
	}{{secret, 1}, {"admin", 2}} {
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		r.Header.Set("Authorization", "Bearer "+tc.key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var response struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || len(response.Data) != tc.count {
			t.Fatalf("model list: %d %s", w.Code, w.Body.String())
		}
		if tc.count == 1 && response.Data[0].ID != "cn:glm-5.2" {
			t.Fatal("wrong model listed")
		}
	}
}
