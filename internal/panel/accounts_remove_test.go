package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

func TestAccountsRemoveDisabled(t *testing.T) {
	dir := t.TempDir()
	accounts := pool.New("")
	for _, uid := range []string{"disabled", "healthy", "paused"} {
		a := &auth.Auth{UID: uid, AccessToken: "at", FilePath: filepath.Join(dir, "workbuddy-"+uid+".json")}
		if err := a.SaveAtomic(); err != nil {
			t.Fatal(err)
		}
		accounts.Add(a)
	}
	accounts.Disable("disabled", "restricted")
	accounts.Pause("paused")
	blocked := filepath.Join(dir, "blocked.json")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "blocker"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	accounts.Add(&auth.Auth{UID: "failed", FilePath: blocked})
	accounts.Disable("failed", "restricted")
	p := New(Config{Pool: accounts, APIKey: "admin"})
	call := func(method, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/panel/api/accounts/remove_disabled", nil)
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}
	for _, key := range []string{"", "wrong-key"} {
		if w := call("POST", key); w.Code != http.StatusUnauthorized {
			t.Fatalf("non-admin request: %d %s", w.Code, w.Body.String())
		}
		if _, err := os.Stat(filepath.Join(dir, "workbuddy-disabled.json")); err != nil {
			t.Fatalf("unauthenticated request deleted file: %v", err)
		}
	}
	if w := call("GET", "admin"); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d %s", w.Code, w.Body.String())
	}
	w := call("POST", "admin")
	var result struct {
		OK      bool `json:"ok"`
		Removed int  `json:"removed"`
		Failed  int  `json:"failed"`
		Errors  []struct {
			UID   string `json:"uid"`
			Error string `json:"error"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || result.OK || result.Removed != 1 || result.Failed != 1 || len(result.Errors) != 1 || result.Errors[0].UID != "failed" || result.Errors[0].Error == "" {
		t.Fatalf("partial deletion: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "workbuddy-disabled.json")); !os.IsNotExist(err) {
		t.Fatalf("disabled credential retained: %v", err)
	}
	for _, uid := range []string{"healthy", "paused", "failed"} {
		if _, ok := accounts.Status(uid); !ok {
			t.Fatalf("account should remain: %s", uid)
		}
	}
	accounts.Revive("failed")
	w = call("POST", "admin")
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || !result.OK || result.Removed != 0 || result.Failed != 0 || len(result.Errors) != 0 {
		t.Fatalf("empty deletion: %d %s", w.Code, w.Body.String())
	}
}
