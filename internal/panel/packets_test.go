package panel

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
)

func TestPacketManagementAndPublicClaim(t *testing.T) {
	keys, _ := httpauth.OpenKeys(filepath.Join(t.TempDir(), "keys.json"))
	p := New(Config{APIKey: "admin", Keys: keys})
	call := func(method, path, key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = "203.0.113.8:1234"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Real-IP", "203.0.113.8")
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}
	w := call("POST", "/panel/api/red-packets", "admin", `{"title":"<img>","kind":"tokens","mode":"random","total":200,"shares":2,"days":1,"allowed_models":["glm-5.2"]}`)
	if w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	var created httpauth.PacketDetail
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if len(created.Keys) != 0 || len(keys.List()) != 0 || created.Remaining != 2 {
		t.Fatalf("creation generated keys before claim: %+v", created)
	}
	w = call("GET", "/panel/api/red-packets/"+created.ID+"/export", "admin", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"keys":[]`) {
		t.Fatalf("unclaimed packet export: %d %s", w.Code, w.Body.String())
	}
	for _, route := range []string{"/panel/api/red-packets", "/panel/api/red-packets/" + created.ID, "/panel/api/red-packets/" + created.ID + "/export"} {
		if call("GET", route, "", "").Code != 401 {
			t.Fatal("public admin endpoint")
		}
	}
	w = call("GET", "/panel/api/claim/"+created.ClaimToken, "", "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Body.String())
	}
	for _, bad := range []string{"claim_token", "encrypted", "token_hash", "key_id", "used_units", "secret"} {
		if strings.Contains(w.Body.String(), bad) {
			t.Fatal("public information leak")
		}
	}
	w = call("POST", "/panel/api/claim/"+created.ClaimToken, "", "{}")
	if w.Code != 200 {
		t.Fatalf("claim %d %s", w.Code, w.Body.String())
	}
	var claimed httpauth.PacketClaim
	_ = json.Unmarshal(w.Body.Bytes(), &claimed)
	if len(keys.List()) != 1 || claimed.Key.Quota == nil || !claimed.Key.Quota.Claimed {
		t.Fatalf("claim did not create one usable key: %+v", claimed)
	}
	if changed := call("PATCH", "/panel/api/keys/"+claimed.Key.ID, "admin", `{"allowed_models":[]}`); changed.Code != 400 {
		t.Fatalf("token packet model validation: %d %s", changed.Code, changed.Body.String())
	}
	w = call("POST", "/panel/api/claim/"+created.ClaimToken, "", "{}")
	var again httpauth.PacketClaim
	_ = json.Unmarshal(w.Body.Bytes(), &again)
	if !again.Repeated || again.Secret != claimed.Secret {
		t.Fatal("duplicate draw")
	}
	if call("GET", "/panel/api/red-packets", claimed.Secret, "").Code != 401 || call("POST", "/panel/api/red-packets/"+created.ID+"/revoke", claimed.Secret, "{}").Code != 401 {
		t.Fatal("claimant gained admin rights")
	}
	w = call("GET", "/panel/api/red-packets/"+created.ID+"/export", "admin", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), claimed.Secret) {
		t.Fatal("admin export missing")
	}
	for _, body := range []string{`{"kind":"tokens","mode":"equal","total":1,"shares":1,"days":1}`, `{} {}`, `{"extra":1}`} {
		if call("POST", "/panel/api/red-packets", "admin", body).Code != 400 {
			t.Fatal("invalid packet accepted")
		}
	}
	if call("POST", "/panel/api/red-packets/"+created.ID+"/revoke", "admin", "{}").Code != 200 {
		t.Fatal("revoke")
	}
	if call("POST", "/panel/api/claim/"+created.ClaimToken, "", "{}").Code != 410 {
		t.Fatal("revoked claim")
	}
	w = call("GET", "/panel/claim", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `src="/panel/claim.js"`) || !strings.Contains(w.Body.String(), `href="/panel/console.css"`) || strings.Contains(w.Body.String(), `src="app.js"`) {
		t.Fatal("bad standalone claim page")
	}
}
func TestPacketClaimIP(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []string
		want   string
	}{
		{"ipv4", []string{"203.0.113.8"}, "203.0.113.8"},
		{"ipv6", []string{"2001:0db8:0:0::8"}, "2001:db8::8"},
		{"mapped-ipv4", []string{"::ffff:203.0.113.8"}, "203.0.113.8"},
		{"trim-spaces", []string{" 203.0.113.8 "}, "203.0.113.8"},
		{"missing", nil, ""},
		{"empty", []string{""}, ""},
		{"invalid", []string{"not-an-ip"}, ""},
		{"comma-separated", []string{"203.0.113.8, 203.0.113.9"}, ""},
		{"with-port", []string{"203.0.113.8:1234"}, ""},
		{"ipv6-zone", []string{"fe80::1%eth0"}, ""},
		{"multiple-values", []string{"203.0.113.8", "203.0.113.9"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := New(Config{})
			r := httptest.NewRequest("POST", "/panel/api/claim/token", nil)
			r.RemoteAddr = "[::ffff:172.27.0.1]:45678"
			r.Header.Set("X-Forwarded-For", "198.51.100.99")
			for _, value := range tc.values {
				r.Header.Add("X-Real-IP", value)
			}
			if got := p.claimIP(r); got != tc.want {
				t.Fatalf("IP=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestPacketClaimViaRealIP(t *testing.T) {
	keys, err := httpauth.OpenKeys(filepath.Join(t.TempDir(), "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	packet, err := keys.CreatePacket(httpauth.PacketInput{Kind: "tokens", Mode: "equal", Total: 99, Shares: 3, Days: 1, AllowedModels: []string{"glm-5.2"}})
	if err != nil {
		t.Fatal(err)
	}
	p := New(Config{APIKey: "admin", Keys: keys})
	claim := func(real, peer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/panel/api/claim/"+packet.ClaimToken, strings.NewReader("{}"))
		r.RemoteAddr = peer
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Forwarded-For", "198.51.100.99")
		if real != "" {
			r.Header.Set("X-Real-IP", real)
		}
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}
	for _, real := range []string{"", "invalid", "203.0.113.8, 203.0.113.9"} {
		w := claim(real, "172.27.0.1:1234")
		if w.Code != 400 || !strings.Contains(w.Body.String(), "X-Real-IP") {
			t.Fatalf("invalid header did not return actionable error: %d %s", w.Code, w.Body.String())
		}
	}
	info, err := keys.Packet(packet.ID)
	if err != nil || info.Claimed != 0 || len(keys.List()) != 0 {
		t.Fatal("missing or invalid real IP consumed a share")
	}
	decode := func(real, peer string) httpauth.PacketClaim {
		t.Helper()
		w := claim(real, peer)
		if w.Code != 200 {
			t.Fatalf("claim status=%d body=%s", w.Code, w.Body.String())
		}
		var result httpauth.PacketClaim
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := decode("203.0.113.8", "172.27.0.1:1234")
	second := decode("203.0.113.9", "172.27.0.1:5678")
	if first.Repeated || second.Repeated || first.Key.ID == second.Key.ID || first.Secret == second.Secret {
		t.Fatal("different clients behind one gateway shared a claim")
	}
	// Docker gateway changes and IPv4-mapped notation must not bypass deduplication.
	again := decode("::ffff:203.0.113.8", "172.28.0.1:9000")
	if !again.Repeated || again.Key.ID != first.Key.ID || again.Secret != first.Secret {
		t.Fatal("same real IP drew another key after the gateway changed")
	}
	third := decode("2001:db8::8", "172.28.0.1:9001")
	if third.Repeated || third.Key.ID == first.Key.ID || third.Key.ID == second.Key.ID {
		t.Fatal("IPv6 client did not receive its own share")
	}
	info, err = keys.Packet(packet.ID)
	if err != nil || info.Claimed != 3 || info.Remaining != 0 || len(keys.List()) != 3 {
		t.Fatalf("bad claim accounting: %+v %v", info, err)
	}
}
