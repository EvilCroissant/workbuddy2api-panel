package panel

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/keyusage"
	"github.com/linguo2625469/workbuddy2api-panel/internal/reqlog"
)

func TestKeyUsageEndpoints(t *testing.T) {
	dir := t.TempDir()
	keys, err := httpauth.OpenKeys(filepath.Join(dir, "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	alice, a, err := keys.Create("Alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, _, err := keys.Create("Bob")
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := keyusage.Open(filepath.Join(dir, "key-usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ledger.Record(reqlog.Event{Time: now, KeyID: alice.ID, RequestID: "r1", Model: "glm-5.2", Status: 200, OK: true, Outcome: reqlog.OutcomeSuccess, PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14, TokenKnown: true})
	ledger.Record(reqlog.Event{Time: now.Add(-48 * time.Hour), KeyID: alice.ID, RequestID: "r2", Model: "glm-5.2", Status: 503, Outcome: reqlog.OutcomeHTTPError})
	ledger.Record(reqlog.Event{Time: now, KeyID: bob.ID, RequestID: "r3", Model: "glm-5.2", Status: 200, OK: true, Outcome: reqlog.OutcomeSuccess, TotalTokens: 99, TokenKnown: true})
	p := New(Config{APIKey: "admin", Keys: keys, KeyUsage: ledger})
	call := func(path, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/panel/api/"+path, nil)
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"keys?hours=24", "keys/" + alice.ID + "/usage?hours=24"} {
		if w := call(path, a); w.Code != 401 {
			t.Fatalf("child viewed monitoring: %d", w.Code)
		}
		if w := call(path, ""); w.Code != 401 {
			t.Fatal("anonymous monitoring")
		}
	}
	for _, hours := range []string{"-1", "1", "garbage", "21474836480"} {
		for _, path := range []string{"keys", "keys/" + alice.ID + "/usage"} {
			if w := call(path+"?hours="+hours, "admin"); w.Code != 400 {
				t.Fatalf("bad range: %d %s", w.Code, w.Body)
			}
		}
	}
	w := call("keys?hours=24", "admin")
	var list struct {
		Keys      []httpauth.KeyInfo
		Usage     map[string]struct{ Requests int64 }
		Available bool `json:"usage_available"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(list.Keys) != 2 || !list.Available || list.Usage[alice.ID].Requests != 1 || list.Usage[bob.ID].Requests != 1 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("list=%s", w.Body)
	}
	check := func(path string, want int64) {
		t.Helper()
		w := call(path, "admin")
		var detail struct {
			Key   httpauth.KeyInfo
			Usage struct {
				Totals struct {
					Requests int64
					Tokens   int64 `json:"total_tokens"`
				}
				Recent []reqlog.Event
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || detail.Key.ID != alice.ID || detail.Usage.Totals.Requests != want || detail.Usage.Totals.Tokens != 14 {
			t.Fatalf("detail=%s", w.Body)
		}
		for _, e := range detail.Usage.Recent {
			if e.KeyID != alice.ID || e.RequestID == "r3" {
				t.Fatalf("cross-key detail: %+v", e)
			}
		}
		if strings.Contains(w.Body.String(), a) {
			t.Fatal("plaintext secret in usage response")
		}
	}
	check("keys/"+alice.ID+"/usage?hours=24", 1)
	check("keys/"+alice.ID+"/usage", 2)
	if err := keys.SetEnabled(alice.ID, false); err != nil {
		t.Fatal(err)
	}
	check("keys/"+alice.ID+"/usage", 2)
	if err := keys.Delete(alice.ID); err != nil {
		t.Fatal(err)
	}
	check("keys/"+alice.ID+"/usage", 2)
	if w := call("keys/missing/usage", "admin"); w.Code != 404 {
		t.Fatal("unknown key detail")
	}
	unavailable := New(Config{APIKey: "admin", Keys: keys})
	r := httptest.NewRequest("GET", "/panel/api/keys/"+bob.ID+"/usage", nil)
	r.Header.Set("Authorization", "Bearer admin")
	w = httptest.NewRecorder()
	unavailable.ServeHTTP(w, r)
	if w.Code != 501 {
		t.Fatalf("disabled monitoring=%d", w.Code)
	}
}
