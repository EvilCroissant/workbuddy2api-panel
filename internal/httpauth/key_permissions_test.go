package httpauth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestKeyPermissionsAndRevealPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	s, err := OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	info, secret, err := s.CreateWithModels("client", []string{"glm-5.2", "cn:glm-5.2", "global:other"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cn:glm-5.2", "global:other"}
	if !reflect.DeepEqual(info.AllowedModels, want) {
		t.Fatalf("models=%v", info.AllowedModels)
	}
	info.AllowedModels[0] = "cn:mutated"
	listed := s.List()
	listed[0].AllowedModels[0] = "cn:also-mutated"
	if !s.AllowsModel(info.ID, "cn", "glm-5.2") || s.AllowsModel(info.ID, "global", "glm-5.2") || s.AllowsModel(info.ID, "cn", "other") {
		t.Fatal("model permissions bypassed or mutated through metadata")
	}
	s, err = OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Reveal(info.ID)
	if err != nil || got != secret {
		t.Fatalf("reveal after restart: %v", err)
	}
	for _, file := range []string{path, path + ".key"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), secret) {
			t.Fatal("plaintext persisted")
		}
		st, err := os.Stat(file)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatal("secret files must have mode 0600")
		}
	}
	meta, _ := json.Marshal(s.List())
	if strings.Contains(string(meta), secret) || strings.Contains(string(meta), "encrypted_secret") {
		t.Fatal("list exposed secret")
	}
	deny := []string{"cn:different"}
	if err := s.Update(info.ID, nil, &deny); err != nil {
		t.Fatal(err)
	}
	if s.AllowsModel(info.ID, "cn", "glm-5.2") || !s.AllowsModel(info.ID, "cn", "different") {
		t.Fatal("policy update did not take effect")
	}
	all := []string{}
	if err := s.Update(info.ID, nil, &all); err != nil {
		t.Fatal(err)
	}
	if !s.AllowsModel(info.ID, "global", "any") {
		t.Fatal("empty policy should allow all models")
	}
}

func TestLegacyKeyRotationPreservesIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	old := "sk-wb-old-secret"
	hash := sha256.Sum256([]byte(old))
	b, _ := json.Marshal([]storedKey{{KeyInfo: KeyInfo{ID: "legacy", Name: "client", Enabled: true, AllowedModels: []string{"cn:glm-5.2"}}, Hash: hex.EncodeToString(hash[:])}})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyAPI(req("Bearer "+old), "admin", s) {
		t.Fatal("legacy token rejected")
	}
	if _, err := s.Reveal("legacy"); !errors.Is(err, ErrKeySecretUnavailable) {
		t.Fatalf("legacy reveal: %v", err)
	}
	secret, err := s.Rotate("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if VerifyAPI(req("Bearer "+old), "admin", s) || !VerifyAPI(req("Bearer "+secret), "admin", s) {
		t.Fatal("rotation did not revoke only old token")
	}
	if id, ok := IdentifyAPI(req("Bearer "+secret), "admin", s); !ok || id != "legacy" {
		t.Fatal("usage identity changed")
	}
	if !s.AllowsModel("legacy", "cn", "glm-5.2") || s.AllowsModel("legacy", "global", "glm-5.2") {
		t.Fatal("rotation changed permissions")
	}
	s, err = OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Reveal("legacy"); err != nil || got != secret {
		t.Fatalf("rotated reveal: %v", err)
	}
}

func TestKeyPermissionValidationAndMissingEncryptionFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	s, err := OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, models := range [][]string{{""}, {"cn:"}, {"global:"}, {"bad id"}, {"a\u2003b"}, {strings.Repeat("a", 198)}, make([]string, 129)} {
		if _, _, err := s.CreateWithModels("client", models); !errors.Is(err, ErrKeyModels) {
			t.Fatalf("invalid models accepted: %v", err)
		}
	}
	if _, _, err := s.Create("client"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + ".key"); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenKeys(path); err == nil {
		t.Fatal("missing encryption file silently accepted")
	}
}
