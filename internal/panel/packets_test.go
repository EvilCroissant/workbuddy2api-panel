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
	_ = json.Unmarshal(w.Body.Bytes(), &created)
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
func TestPacketClaimIPTrust(t *testing.T) {
	for _, tc := range []struct {
		peer, xff, real, want string
		trusted               []string
	}{
		{"203.0.113.1:100", "1.2.3.4", "", "203.0.113.1", nil},
		{"127.0.0.1:100", "203.0.113.8", "", "203.0.113.8", []string{"127.0.0.1/32"}},
		{"127.0.0.1:100", "1.2.3.4, 203.0.113.8", "", "203.0.113.8", []string{"127.0.0.1/32"}},
		{"127.0.0.1:100", "1.2.3.4, 10.1.0.4", "", "1.2.3.4", []string{"127.0.0.1/32", "10.1.0.0/16"}},
		{"127.0.0.1:100", "bad", "", "127.0.0.1", []string{"127.0.0.1/32"}},
		{"[::ffff:203.0.113.8]:10", "", "", "203.0.113.8", nil},
	} {
		p := New(Config{ClaimTrustedProxies: tc.trusted})
		r := httptest.NewRequest("POST", "/panel/api/claim/token", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-For", tc.xff)
		r.Header.Set("X-Real-IP", tc.real)
		if got := p.claimIP(r); got != tc.want {
			t.Fatalf("peer=%s xff=%s got=%s want=%s", tc.peer, tc.xff, got, tc.want)
		}
	}
}
