package httpauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestDistributedKeysLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "api-keys.json")
	s, err := OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyAPI(req(""), "", s) || !VerifyAdmin(req(""), "", s) {
		t.Fatal("legacy open mode")
	}
	first, one, err := s.Create(" Alice ")
	if err != nil {
		t.Fatal(err)
	}
	second, two, err := s.Create("Bob")
	if err != nil {
		t.Fatal(err)
	}
	if one == two || first.ID == second.ID || first.Name != "Alice" {
		t.Fatal("nonunique keys or name not normalized")
	}
	for _, token := range []string{one, two, "admin"} {
		if !VerifyAPI(req("Bearer "+token), "admin", s) {
			t.Fatalf("valid API token rejected: %s", token[:5])
		}
	}
	for _, token := range []string{"", "Bearer wrong", one, "bearer " + one, "Bearer  " + one} {
		if VerifyAPI(req(token), "admin", s) {
			t.Fatal("invalid token accepted")
		}
	}
	if VerifyAdmin(req("Bearer "+one), "admin", s) || VerifyAdmin(req(""), "", s) || VerifyAPI(req(""), "", s) {
		t.Fatal("distributed keys must not grant admin or anonymous access")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), one) || strings.Contains(string(b), two) {
		t.Fatal("plaintext key persisted")
	}
	meta, _ := json.Marshal(s.List())
	if strings.Contains(string(meta), "hash") || strings.Contains(string(meta), one) {
		t.Fatal("key list leaks credentials")
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("file permissions: %o", st.Mode().Perm())
	}
	if err := s.SetEnabled(first.ID, false); err != nil {
		t.Fatal(err)
	}
	s, err = OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	if VerifyAPI(req("Bearer "+one), "admin", s) || !VerifyAPI(req("Bearer "+two), "admin", s) {
		t.Fatal("revocation not isolated/persistent")
	}
	if err := s.SetEnabled(second.ID, false); err != nil {
		t.Fatal(err)
	}
	if VerifyAPI(req(""), "", s) {
		t.Fatal("disabled keys must not enable anonymous access")
	}
	if err := s.SetEnabled(first.ID, true); err != nil {
		t.Fatal(err)
	}
	if !VerifyAPI(req("Bearer "+one), "admin", s) {
		t.Fatal("reenable failed")
	}
	if err := s.Delete(first.ID); err != nil {
		t.Fatal(err)
	}
	s, err = OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	if VerifyAPI(req("Bearer "+one), "admin", s) {
		t.Fatal("deleted key survives restart")
	}
	if err := s.Delete(first.ID); err != ErrKeyNotFound {
		t.Fatalf("missing key: %v", err)
	}
	if err := s.SetEnabled(first.ID, true); err != ErrKeyNotFound {
		t.Fatalf("missing key: %v", err)
	}
}

func TestDistributedKeyFailuresAndConcurrency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	s, err := OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "  ", strings.Repeat("人", 81)} {
		if _, _, err := s.Create(name); err == nil {
			t.Fatal("invalid name accepted")
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, secret, err := s.Create("parallel")
			if err != nil {
				t.Error(err)
				return
			}
			if !VerifyAPI(req("Bearer "+secret), "admin", s) {
				t.Error("concurrent key lost")
			}
			_ = s.List()
		}()
	}
	wg.Wait()
	reloaded, err := OpenKeys(path)
	if err != nil || len(reloaded.List()) != 12 {
		t.Fatalf("lost updates: %v", err)
	}
	info := s.List()[0]
	// Replacing the file with a directory forces Rename failure on every platform.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnabled(info.ID, false); err == nil {
		t.Fatal("failed write reported success")
	}
	if !s.List()[0].Enabled {
		t.Fatal("failed update changed live state")
	}
	if err := s.Delete(info.ID); err == nil || len(s.List()) != 12 {
		t.Fatal("failed deletion changed live state")
	}
	if _, _, err := s.Create("failed"); err == nil || len(s.List()) != 12 {
		t.Fatal("failed creation changed live state")
	}
}

func TestDistributedKeysCorruptFileFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	for _, content := range []string{"broken", "null", `{}`, `[{"id":"one","hash":"invalid"}]`, `[{"hash":"` + strings.Repeat("0", 64) + `"}]`} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenKeys(path); err == nil {
			t.Fatalf("corrupt file accepted: %s", content)
		}
	}
}

func TestIdentifyAPIUsesStablePublicID(t *testing.T) {
	s, err := OpenKeys(filepath.Join(t.TempDir(), "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	info, key, err := s.Create("Alice")
	if err != nil {
		t.Fatal(err)
	}
	id, ok := IdentifyAPI(req("Bearer "+key), "admin", s)
	if !ok || id != info.ID {
		t.Fatalf("id=%q ok=%v", id, ok)
	}
	for _, token := range []string{"admin", "wrong", ""} {
		id, ok := IdentifyAPI(req("Bearer "+token), "admin", s)
		if id != "" || ok != (token == "admin") {
			t.Fatalf("non-child identity: %q %v", id, ok)
		}
	}
	// Administrator matching takes precedence even if manually configured to equal a child secret.
	if id, ok := IdentifyAPI(req("Bearer "+key), key, s); !ok || id != "" {
		t.Fatal("admin counted as child")
	}
	if err := s.SetEnabled(info.ID, false); err != nil {
		t.Fatal(err)
	}
	if id, ok := IdentifyAPI(req("Bearer "+key), "admin", s); ok || id != "" {
		t.Fatal("disabled key identified")
	}
	if id, ok := IdentifyAPI(req(""), "", nil); !ok || id != "" {
		t.Fatal("anonymous identity")
	}
}
