package panel

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/livecfg"
)

func TestKeyManagementAndAdminIsolation(t *testing.T) {
	s, err := httpauth.OpenKeys(filepath.Join(t.TempDir(), "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	live := livecfg.New(livecfg.Snapshot{APIKey: "admin"})
	saved := false
	p := New(Config{Keys: s, Live: live, SaveConfig: func(raw []byte) ([]string, error) { saved = true; return nil, nil }})
	call := func(method, path, key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/panel/api/"+path, strings.NewReader(body))
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}
	w := call("POST", "keys", "admin", `{"name":"Alice"}`)
	if w.Code != 201 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Key    httpauth.KeyInfo `json:"key"`
		Secret string           `json:"secret"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Secret == "" || created.Key.ID == "" {
		t.Fatal("missing new key")
	}
	for _, path := range []string{"keys", "keys/" + created.Key.ID + "/secret", "config", "overview", "logs", "login/regions"} {
		if w := call("GET", path, created.Secret, ""); w.Code != 401 {
			t.Fatalf("distributed key accessed %s: %d", path, w.Code)
		}
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "keys", `{"name":"escalate"}`},
		{"PATCH", "keys/" + created.Key.ID, `{"enabled":false}`},
		{"DELETE", "keys/" + created.Key.ID, ""},
		{"POST", "keys/" + created.Key.ID + "/rotate", ""},
		{"POST", "config", `{"api_key":"hijack"}`},
	} {
		if w := call(c.method, c.path, created.Secret, c.body); w.Code != 401 {
			t.Fatalf("distributed write %s: %d", c.path, w.Code)
		}
	}
	w = call("GET", "keys", "admin", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), created.Secret) || strings.Contains(w.Body.String(), `"hash"`) {
		t.Fatalf("list leak: %s", w.Body.String())
	}
	for _, body := range []string{`{"api_key":""}`, `{"api_key":null}`} {
		if w := call("POST", "config", "admin", body); w.Code != 400 || saved {
			t.Fatal("administrator key cleared with distributed keys")
		}
	}
	for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":true,"extra":1}`, `{"enabled":false} {}`, `{"enabled":"no"}`} {
		if w := call("PATCH", "keys/"+created.Key.ID, "admin", body); w.Code != 400 {
			t.Fatalf("invalid patch %s accepted: %d", body, w.Code)
		}
	}
	w = call("PATCH", "keys/"+created.Key.ID, "admin", `{"enabled":false}`)
	if w.Code != 200 || s.List()[0].Enabled {
		t.Fatal("disable failed")
	}
	w = call("PATCH", "keys/"+created.Key.ID, "admin", `{"enabled":true}`)
	if w.Code != 200 || !s.List()[0].Enabled {
		t.Fatal("enable failed")
	}
	live.Store(livecfg.Snapshot{APIKey: "rotated-admin"})
	if w := call("GET", "keys", "admin", ""); w.Code != 401 {
		t.Fatal("old administrator key accepted")
	}
	if w := call("GET", "keys", "rotated-admin", ""); w.Code != 200 {
		t.Fatal("new administrator key rejected")
	}
	live.Store(livecfg.Snapshot{})
	if w := call("GET", "keys", "", ""); w.Code != 401 {
		t.Fatal("missing administrator key opened panel")
	}
	live.Store(livecfg.Snapshot{APIKey: "admin"})
	if w := call("DELETE", "keys/"+created.Key.ID, "admin", ""); w.Code != 200 {
		t.Fatal("delete failed")
	}
	if w := call("DELETE", "keys/"+created.Key.ID, "admin", ""); w.Code != 404 {
		t.Fatal("delete nonexistent key")
	}
	if w := call("POST", "config", "admin", `{"api_key":""}`); w.Code != 200 || !saved {
		t.Fatal("legacy open mode blocked after deleting all keys")
	}
	live.Store(livecfg.Snapshot{})
	if w := call("POST", "keys", "", `{"name":"Alice"}`); w.Code != 409 {
		t.Fatal("created key without administrator credential")
	}
}

func TestKeyModelManagementAndSecretRetrieval(t *testing.T) {
	keys, err := httpauth.OpenKeys(filepath.Join(t.TempDir(), "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	p := New(Config{Keys: keys, APIKey: "admin"})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/panel/api/"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer admin")
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}
	w := call("POST", "keys", `{"name":"client","allowed_models":["glm-5.2"]}`)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Key    httpauth.KeyInfo `json:"key"`
		Secret string           `json:"secret"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	path := "keys/" + created.Key.ID
	w = call("GET", path+"/secret", "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), created.Secret) {
		t.Fatalf("reveal: %d %s", w.Code, w.Body.String())
	}
	w = call("PATCH", path, `{"allowed_models":["global:glm-5.2"]}`)
	if w.Code != 200 || keys.AllowsModel(created.Key.ID, "cn", "glm-5.2") || !keys.AllowsModel(created.Key.ID, "global", "glm-5.2") {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	w = call("PATCH", path, `{"allowed_models":["bad model"]}`)
	if w.Code != 400 || !keys.AllowsModel(created.Key.ID, "global", "glm-5.2") {
		t.Fatal("invalid policy changed permissions")
	}
	w = call("POST", path+"/rotate", "")
	var rotated struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || rotated.Secret == "" || rotated.Secret == created.Secret {
		t.Fatalf("rotation: %d", w.Code)
	}
	if !keys.AllowsModel(created.Key.ID, "global", "glm-5.2") {
		t.Fatal("rotation lost policy")
	}
	w = call("GET", "keys", "")
	if strings.Contains(w.Body.String(), rotated.Secret) || strings.Contains(w.Body.String(), "encrypted_secret") {
		t.Fatal("list leaked key secret")
	}
}
