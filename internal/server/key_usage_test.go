package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/keyusage"
	"github.com/linguo2625469/workbuddy2api-panel/internal/reqlog"
)

func TestKeyUsageAttributionAndRevocation(t *testing.T) {
	keys, err := httpauth.OpenKeys(filepath.Join(t.TempDir(), "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	alice, a, err := keys.Create("Alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, b, err := keys.Create("Bob")
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := keyusage.Open(filepath.Join(t.TempDir(), "usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Stop()
	// Deliberately omit RequestLog: Key monitoring must also work with logging disabled.
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true })
	h := NewHandler(Config{APIKey: "admin", Keys: keys, KeyUsage: ledger,
		Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}), Upstream: up})
	call := func(method, path, key, body string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	body := `{"model":"glm-5.2","messages":[]}`
	if call("POST", "/v1/chat/completions", a, body) != 200 {
		t.Fatal("Alice sync request failed")
	}
	if call("POST", "/v1/chat/completions", b, `{"model":"glm-5.2","stream":true,"messages":[]}`) != 200 {
		t.Fatal("Bob stream request failed")
	}
	if call("POST", "/v1/chat/completions", "admin", body) != 200 {
		t.Fatal("admin request failed")
	}
	if call("POST", "/v1/chat/completions", "bad", body) != 401 {
		t.Fatal("bad key accepted")
	}
	if call("GET", "/v1/models", a, "") != 200 || call("GET", "/status", a, "") != 401 {
		t.Fatal("route permissions")
	}
	if err := keys.SetEnabled(alice.ID, false); err != nil {
		t.Fatal(err)
	}
	if call("POST", "/v1/chat/completions", a, body) != 401 {
		t.Fatal("disabled key accepted")
	}
	if err := keys.Delete(bob.ID); err != nil {
		t.Fatal(err)
	}
	if call("POST", "/v1/chat/completions", b, body) != 401 {
		t.Fatal("deleted key accepted")
	}
	stats := ledger.Summary(0)
	if len(stats) != 2 {
		t.Fatalf("unattributed traffic entered ledger: %+v", stats)
	}
	for _, id := range []string{alice.ID, bob.ID} {
		s := stats[id]
		raw, _ := json.Marshal(s)
		var v struct {
			Requests, Errors int64
			TotalTokens      int64 `json:"total_tokens"`
			Known            int64 `json:"token_requests"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		if v.Requests != 1 || v.Errors != 0 || v.TotalTokens != 2 || v.Known != 1 {
			t.Fatalf("key stats=%s", raw)
		}
	}
	if err := ledger.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestKeyUsageRetryAndStreamFailure(t *testing.T) {
	for _, tc := range []struct {
		name, response       string
		wantError, wantKnown bool
	}{
		{"retry", sseOK, false, true},
		{"stream error", "data: {\"error\":{\"message\":\"failed\"}}\n\ndata: [DONE]\n\n", true, false},
		{"missing usage", "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys, _ := httpauth.OpenKeys(filepath.Join(t.TempDir(), "keys.json"))
			info, key, err := keys.Create("test")
			if err != nil {
				t.Fatal(err)
			}
			ledger, err := keyusage.Open("")
			if err != nil {
				t.Fatal(err)
			}
			logs := reqlog.New(reqlog.Config{Enabled: false})
			defer logs.Close()
			calls := 0
			up := newFakeUpstream(t, func(authz string) (int, string, bool) {
				calls++
				if authz == "Bearer bad" {
					return 402, `{"code":1,"msg":"余额不足"}`, false
				}
				return 200, tc.response, true
			})
			p := testPoolWith(&auth.Auth{UID: "bad", AccessToken: "bad", ExpiresAt: 9999999999}, &auth.Auth{UID: "good", AccessToken: "good", ExpiresAt: 9999999999})
			p.SetCredits("bad", 2000, 0)
			h := NewHandler(Config{APIKey: "admin", Keys: keys, KeyUsage: ledger, RequestLog: logs, Pool: p, Upstream: up})
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`))
			r.Header.Set("Authorization", "Bearer "+key)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			events := logs.Snapshot().Recent
			if calls != 2 || len(events) != 1 {
				t.Fatalf("attempts=%d events=%+v", calls, events)
			}
			e := events[0]
			if e.KeyID != info.ID || e.Attempts != 2 || e.OK == tc.wantError || e.TokenKnown != tc.wantKnown {
				t.Fatalf("event=%+v", e)
			}
			raw, _ := json.Marshal(ledger.Summary(0)[info.ID])
			var got struct {
				Requests, Errors int64
				Known            int64 `json:"token_requests"`
			}
			_ = json.Unmarshal(raw, &got)
			if got.Requests != 1 || (got.Errors == 1) != tc.wantError || (got.Known == 1) != tc.wantKnown {
				t.Fatalf("stats=%s", raw)
			}
		})
	}
}

func TestKeyUsageSearchHasNoInventedTokens(t *testing.T) {
	keys, _ := httpauth.OpenKeys(filepath.Join(t.TempDir(), "keys.json"))
	info, key, err := keys.Create("search")
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := keyusage.Open("")
	if err != nil {
		t.Fatal(err)
	}
	up := newSearchUpstream(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(searchOKBody)) })
	h := NewHandler(Config{APIKey: "admin", Keys: keys, KeyUsage: ledger, Upstream: up, Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})})
	w := doMessages(t, h, key, messagesBody("golang", true))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	raw, _ := json.Marshal(ledger.Snapshot(info.ID, 0))
	var snap struct {
		Totals struct {
			Requests, Errors int64
			Known            int64 `json:"token_requests"`
			Tokens           int64 `json:"total_tokens"`
		}
		Recent []reqlog.Event
	}
	_ = json.Unmarshal(raw, &snap)
	if snap.Totals.Requests != 1 || snap.Totals.Errors != 0 || snap.Totals.Known != 0 || snap.Totals.Tokens != 0 || len(snap.Recent) != 1 || snap.Recent[0].Model != searchToolLabel {
		t.Fatalf("snapshot=%s", raw)
	}
}
