package httpauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

var ErrKeyNotFound = errors.New("key not found")
var ErrKeyName = errors.New("名称需为 1–80 个字符")
var ErrKeyModels = errors.New("最多允许 128 个模型，每个模型 ID 需为 1–200 个非空白字符")
var ErrKeySecretUnavailable = errors.New("旧密钥仅保存摘要，请重新生成后查看")

// KeyInfo contains metadata only; secrets require a separate administrator request.
type KeyInfo struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Prefix          string    `json:"prefix"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
	AllowedModels   []string  `json:"allowed_models,omitempty"`
	SecretAvailable bool      `json:"secret_available"`
	Quota           *KeyQuota `json:"quota,omitempty"`
}

type storedKey struct {
	KeyInfo
	Hash            string `json:"hash"`
	EncryptedSecret string `json:"encrypted_secret,omitempty"`
}

// KeyStore persists digests and encrypted secrets beside the account state file. Mutations become
// visible only after a successful atomic write, so failed revocations are reported.
type KeyStore struct {
	mu            sync.RWMutex
	path          string
	keys          []storedKey
	secretKey     []byte
	packets       []storedPacket
	quotaInFlight map[string]int
	quotaDirty    bool // accepted settlements waiting for a successful save
}

func OpenKeys(path string) (*KeyStore, error) {
	s := &KeyStore{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var decodeErr error
	if strings.HasPrefix(strings.TrimSpace(string(b)), "{") {
		var disk keyFile
		decodeErr = json.Unmarshal(b, &disk)
		if decodeErr == nil && disk.Version != 2 && disk.Version != 3 {
			decodeErr = errors.New("unsupported distributed key file version")
		}
		s.keys, s.packets = disk.Keys, disk.Packets
	} else {
		decodeErr = json.Unmarshal(b, &s.keys)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("decode distributed keys: %w", decodeErr)
	}
	if s.keys == nil {
		return nil, errors.New("distributed keys must be a JSON array")
	}
	seen := make(map[string]bool)
	for _, k := range s.keys {
		hash, err := hex.DecodeString(k.Hash)
		if k.ID == "" || seen[k.ID] || err != nil || len(hash) != sha256.Size {
			return nil, errors.New("invalid distributed key record")
		}
		seen[k.ID] = true
	}
	for i := range s.keys {
		k := &s.keys[i]
		models, err := normalizeKeyModels(k.AllowedModels)
		if err != nil {
			return nil, err
		}
		k.AllowedModels = models
		k.SecretAvailable = k.EncryptedSecret != ""
		if k.Quota != nil {
			if err := validateStoredQuota(k.Quota); err != nil {
				return nil, err
			}
			// An interrupted request is an accounting warning, not a permanent ban.
			if k.Quota.Pending {
				k.Quota.Uncertain = true
				k.Quota.Pending = false
				k.Quota.UnsettledRequests++
				s.quotaDirty = true
			}
			if k.Quota.Uncertain && k.Quota.UnsettledRequests == 0 {
				k.Quota.UnsettledRequests = 1 // legacy "待核对" record
				s.quotaDirty = true
			}
		}
		if k.SecretAvailable {
			if _, err := s.decryptSecret(*k); err != nil {
				return nil, fmt.Errorf("decode distributed key secret: %w", err)
			}
		}
	}
	if err := s.validatePackets(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *KeyStore) List() []KeyInfo {
	out := []KeyInfo{}
	if s == nil {
		return out
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range s.keys {
		info := k.KeyInfo
		info.AllowedModels = append([]string(nil), info.AllowedModels...)
		if info.Quota != nil {
			q := *info.Quota
			info.Quota = &q
		}
		out = append(out, info)
	}
	return out
}

func (s *KeyStore) HasKeys() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.keys) > 0
}

func (s *KeyStore) Create(name string) (KeyInfo, string, error) {
	return s.CreateWithModels(name, nil)
}

func (s *KeyStore) CreateWithModels(name string, models []string) (KeyInfo, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 80 {
		return KeyInfo{}, "", ErrKeyName
	}
	models, err := normalizeKeyModels(models)
	if err != nil {
		return KeyInfo{}, "", err
	}
	var random [44]byte
	if _, err := rand.Read(random[:]); err != nil {
		return KeyInfo{}, "", err
	}
	secret := "sk-wb-" + hex.EncodeToString(random[:32])
	hash := sha256.Sum256([]byte(secret))
	info := KeyInfo{ID: hex.EncodeToString(random[32:]), Name: name,
		Prefix: secret[:14], Enabled: true, CreatedAt: time.Now().UTC(), AllowedModels: models, SecretAvailable: true}
	s.mu.Lock()
	defer s.mu.Unlock()
	encrypted, err := s.encryptSecret(info.ID, secret)
	if err != nil {
		return KeyInfo{}, "", err
	}
	keys := append(append([]storedKey{}, s.keys...), storedKey{KeyInfo: info, Hash: hex.EncodeToString(hash[:]), EncryptedSecret: encrypted})
	if err := s.save(keys); err != nil {
		return KeyInfo{}, "", err
	}
	info.AllowedModels = append([]string(nil), info.AllowedModels...)
	return info, secret, nil
}

func (s *KeyStore) SetEnabled(id string, enabled bool) error {
	return s.Update(id, &enabled, nil)
}

func (s *KeyStore) Update(id string, enabled *bool, models *[]string) error {
	var normalized []string
	if models != nil {
		var err error
		normalized, err = normalizeKeyModels(*models)
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := append([]storedKey{}, s.keys...)
	for i := range keys {
		if keys[i].ID == id {
			if keys[i].Quota != nil && models != nil && keys[i].Quota.Kind == "tokens" && len(normalized) == 0 {
				return ErrPacketModels
			}
			if enabled != nil {
				keys[i].Enabled = *enabled
			}
			if models != nil {
				keys[i].AllowedModels = normalized
			}
			return s.save(keys)
		}
	}
	return ErrKeyNotFound
}

func normalizeKeyModels(models []string) ([]string, error) {
	if len(models) > 128 {
		return nil, ErrKeyModels
	}
	out := []string{}
	seen := map[string]bool{}
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || strings.IndexFunc(model, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return nil, ErrKeyModels
		}
		if !strings.HasPrefix(model, "cn:") && !strings.HasPrefix(model, "global:") {
			model = "cn:" + model
		}
		if len(model) > 200 || model == "cn:" || model == "global:" {
			return nil, ErrKeyModels
		}
		if !seen[model] {
			out = append(out, model)
			seen[model] = true
		}
	}
	return out, nil
}

// AllowsModel receives the resolved realm and model ID, so aliases cannot bypass it.
func (s *KeyStore) AllowsModel(id, realm, model string) bool {
	if id == "" {
		return true
	} // administrator or legacy anonymous access
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range s.keys {
		if k.ID != id {
			continue
		}
		if !keyActive(k) {
			return false
		}
		if len(k.AllowedModels) == 0 {
			return true
		}
		for _, allowed := range k.AllowedModels {
			if allowed == realm+":"+model {
				return true
			}
		}
		return false
	}
	return false
}

func (s *KeyStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]storedKey, 0, len(s.keys))
	for _, k := range s.keys {
		if k.ID != id {
			keys = append(keys, k)
		}
	}
	if len(keys) == len(s.keys) {
		return ErrKeyNotFound
	}
	return s.save(keys)
}

func (s *KeyStore) save(keys []storedKey) error {
	return s.saveState(keys, s.packets)
}

func (s *KeyStore) saveState(keys []storedKey, packets []storedPacket) error {
	if keys == nil {
		keys = []storedKey{}
	}
	var value any = keys
	if len(packets) > 0 {
		version := 2
		for _, p := range packets {
			if p.Allocation != nil {
				version = 3 // Older binaries must not drop allocation counters on save.
				break
			}
		}
		value = keyFile{Version: version, Keys: keys, Packets: packets}
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".api-keys-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(f.Name(), s.path); err != nil {
		return err
	}
	s.keys = keys
	s.packets = packets
	s.quotaDirty = false
	return nil
}

func (s *KeyStore) identify(r *http.Request) (string, bool) {
	if s == nil {
		return "", false
	}
	authz := r.Header.Get("Authorization")
	validScheme := strings.HasPrefix(authz, bearerPrefix)
	token := ""
	if validScheme {
		token = authz[len(bearerPrefix):]
	}
	hash := digest(token)
	s.mu.RLock()
	defer s.mu.RUnlock()
	matched := 0
	id := ""
	for _, k := range s.keys {
		stored, _ := hex.DecodeString(k.Hash) // validated by OpenKeys or generated by Create
		equal := subtle.ConstantTimeCompare(hash, stored)
		if keyActive(k) {
			matched |= equal
			if equal == 1 {
				id = k.ID
			}
		}
	}
	return id, validScheme && matched == 1
}

// VerifyAdmin preserves legacy open mode only when no distributed keys exist.
func VerifyAdmin(r *http.Request, admin string, keys *KeyStore) bool {
	if admin == "" && keys.HasKeys() {
		return false
	}
	return VerifyBearer(r, admin)
}

// VerifyAPI accepts either the original administrator key or an enabled distributed key.
func VerifyAPI(r *http.Request, admin string, keys *KeyStore) bool {
	_, ok := IdentifyAPI(r, admin, keys)
	return ok
}

// IdentifyAPI authenticates once and returns only a stable public key ID.
// Administrators and legacy anonymous traffic have no distributed-key identity.
func IdentifyAPI(r *http.Request, admin string, keys *KeyStore) (string, bool) {
	if admin != "" && VerifyBearer(r, admin) {
		return "", true
	}
	if id, ok := keys.identify(r); ok {
		return id, true
	}
	return "", admin == "" && !keys.HasKeys()
}
