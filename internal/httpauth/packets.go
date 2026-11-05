package httpauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"
)

const CreditScale int64 = 1000000

var (
	ErrPacketInput    = errors.New("红包参数无效：总额需为正数，份数 1–1000，有效期 1–365 天；积分最多六位小数，Token 为整数")
	ErrPacketModels   = errors.New("Token 红包必须至少限定一个模型")
	ErrPacketNotFound = errors.New("红包不存在或链接已失效")
	ErrPacketClosed   = errors.New("红包已过期、领完或被收回")
	ErrQuotaExhausted = errors.New("红包额度已用完")
	ErrQuotaBusy      = errors.New("此红包 Key 仍有请求正在执行，请等待全部请求结算后再核对")
	ErrQuotaUncertain = errors.New("上次请求缺少完整用量，额度已暂停；请联系管理员核对")
)

// KeyQuota is independent of optional usage logging; amounts are integer units.
// Credits use millionths, tokens use whole tokens. Pending stays true while any
// requests are in flight and is persisted before forwarding the first one.
type KeyQuota struct {
	PacketID  string    `json:"packet_id"`
	Kind      string    `json:"kind"`
	Limit     int64     `json:"limit_units"`
	Used      int64     `json:"used_units"`
	ExpiresAt time.Time `json:"expires_at"`
	Claimed   bool      `json:"claimed"`
	Pending   bool      `json:"pending"`
	Uncertain bool      `json:"uncertain"`
}
type PacketInput struct {
	Title         string   `json:"title"`
	Kind          string   `json:"kind"`
	Mode          string   `json:"mode"`
	Total         float64  `json:"total"`
	Shares        int      `json:"shares"`
	Days          int      `json:"days"`
	AllowedModels []string `json:"allowed_models"`
}
type PacketInfo struct {
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	Kind          string     `json:"kind"`
	Mode          string     `json:"mode"`
	Total         float64    `json:"total"`
	Used          float64    `json:"used"`
	Shares        int        `json:"shares"`
	Claimed       int        `json:"claimed"`
	Remaining     int        `json:"remaining"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	AllowedModels []string   `json:"allowed_models,omitempty"`
}
type storedPacket struct {
	PacketInfo
	TokenHash      string            `json:"token_hash"`
	EncryptedToken string            `json:"encrypted_token"`
	Claims         map[string]string `json:"claims"` // salted IP digest -> public key ID
	UsedUnits      int64             `json:"used_units"`
}
type keyFile struct {
	Version int            `json:"version"`
	Keys    []storedKey    `json:"keys"`
	Packets []storedPacket `json:"packets"`
}
type PacketDetail struct {
	PacketInfo
	ClaimToken string    `json:"claim_token"`
	Keys       []KeyInfo `json:"keys"`
}
type PacketClaim struct {
	Key      KeyInfo `json:"key"`
	Secret   string  `json:"secret"`
	Repeated bool    `json:"repeated"`
}

func packetScale(kind string) int64 {
	if kind == "credit" {
		return CreditScale
	}
	return 1
}
func keyActive(k storedKey) bool {
	return k.Enabled && (k.Quota == nil || (k.Quota.Claimed && time.Now().Before(k.Quota.ExpiresAt)))
}
func validateStoredQuota(q *KeyQuota) error {
	if q.PacketID == "" || (q.Kind != "credit" && q.Kind != "tokens") || q.Limit <= 0 || q.Used < 0 || q.ExpiresAt.IsZero() {
		return errors.New("invalid key quota")
	}
	return nil
}
func (s *KeyStore) validatePackets() error {
	seen := map[string]bool{}
	for _, p := range s.packets {
		if p.ID == "" || seen[p.ID] || len(p.TokenHash) != 64 || p.Shares < 1 || p.Shares > 1000 || p.Claims == nil || p.UsedUnits < 0 {
			return errors.New("invalid red packet record")
		}
		seen[p.ID] = true
		// Tokens use the same authenticated encryption as ordinary key secrets.
		token, err := s.decryptSecret(storedKey{KeyInfo: KeyInfo{ID: p.ID}, Hash: p.TokenHash, EncryptedSecret: p.EncryptedToken})
		if err != nil || token == "" {
			return errors.New("invalid encrypted red packet link")
		}
	}
	for _, k := range s.keys {
		if k.Quota != nil && !seen[k.Quota.PacketID] {
			return errors.New("key references missing red packet")
		}
	}
	return nil
}
func randomHex(size int) (string, error) {
	b := make([]byte, size)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}
func cloneKeys(in []storedKey) []storedKey {
	out := append([]storedKey{}, in...)
	for i := range out {
		if out[i].Quota != nil {
			q := *out[i].Quota
			out[i].Quota = &q
		}
	}
	return out
}
func packetAmounts(total int64, count int, mode string) ([]int64, error) {
	amounts := make([]int64, count)
	remaining := total
	for i := range amounts {
		n := int64(count - i)
		amount := remaining / n
		if n == 1 {
			amount = remaining
		} else if mode == "random" {
			upper := 2 * (remaining / n)
			if max := remaining - (n - 1); upper > max {
				upper = max
			}
			value, err := rand.Int(rand.Reader, big.NewInt(upper))
			if err != nil {
				return nil, err
			}
			amount = 1 + value.Int64()
		}
		amounts[i] = amount
		remaining -= amount
	}
	return amounts, nil
}
func (s *KeyStore) CreatePacket(in PacketInput) (PacketDetail, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		in.Title = "送你一个红包"
	}
	if len([]rune(in.Title)) > 60 || (in.Kind != "credit" && in.Kind != "tokens") || (in.Mode != "equal" && in.Mode != "random") || math.IsNaN(in.Total) || math.IsInf(in.Total, 0) || in.Total <= 0 || in.Total > 1e9 || in.Shares < 1 || in.Shares > 1000 || in.Days < 1 || in.Days > 365 {
		return PacketDetail{}, ErrPacketInput
	}
	scaled := in.Total * float64(packetScale(in.Kind))
	units := int64(math.Round(scaled))
	if math.Abs(scaled-float64(units)) > 0.000001 || units < int64(in.Shares) {
		return PacketDetail{}, ErrPacketInput
	}
	models, err := normalizeKeyModels(in.AllowedModels)
	if err != nil {
		return PacketDetail{}, err
	}
	if in.Kind == "tokens" && len(models) == 0 {
		return PacketDetail{}, ErrPacketModels
	}
	amounts, err := packetAmounts(units, in.Shares, in.Mode)
	if err != nil {
		return PacketDetail{}, err
	}
	id, err := randomHex(12)
	if err != nil {
		return PacketDetail{}, err
	}
	token, err := randomHex(32)
	if err != nil {
		return PacketDetail{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sealed, err := s.encryptSecret(id, token)
	if err != nil {
		return PacketDetail{}, err
	}
	hash := sha256.Sum256([]byte(token))
	now := time.Now().UTC()
	p := storedPacket{PacketInfo: PacketInfo{ID: id, Title: in.Title, Kind: in.Kind, Mode: in.Mode, Total: in.Total, Shares: in.Shares, Remaining: in.Shares, CreatedAt: now, ExpiresAt: now.Add(time.Duration(in.Days) * 24 * time.Hour), AllowedModels: models}, TokenHash: hex.EncodeToString(hash[:]), EncryptedToken: sealed, Claims: map[string]string{}}
	keys := cloneKeys(s.keys)
	for i, amount := range amounts {
		random, err := randomHex(44)
		if err != nil {
			return PacketDetail{}, err
		}
		secret := "sk-wb-" + random[:64]
		keyID := random[64:]
		digest := sha256.Sum256([]byte(secret))
		enc, err := s.encryptSecret(keyID, secret)
		if err != nil {
			return PacketDetail{}, err
		}
		info := KeyInfo{ID: keyID, Name: fmt.Sprintf("%.60s · %d", in.Title, i+1), Prefix: secret[:14], Enabled: true, CreatedAt: now, AllowedModels: models, SecretAvailable: true, Quota: &KeyQuota{PacketID: id, Kind: in.Kind, Limit: amount, ExpiresAt: p.ExpiresAt}}
		keys = append(keys, storedKey{KeyInfo: info, Hash: hex.EncodeToString(digest[:]), EncryptedSecret: enc})
	}
	packets := append(append([]storedPacket{}, s.packets...), p)
	if err := s.saveState(keys, packets); err != nil {
		return PacketDetail{}, err
	}
	return s.packetDetailLocked(p, token), nil
}
func (s *KeyStore) packetInfoLocked(p storedPacket) PacketInfo {
	info := p.PacketInfo
	info.AllowedModels = append([]string(nil), info.AllowedModels...)
	info.Claimed = len(p.Claims)
	info.Remaining = 0
	info.Used = float64(p.UsedUnits) / float64(packetScale(p.Kind))
	if p.RevokedAt == nil && time.Now().Before(p.ExpiresAt) {
		for _, k := range s.keys {
			if k.Quota != nil && k.Quota.PacketID == p.ID && !k.Quota.Claimed && k.Enabled {
				info.Remaining++
			}
		}
	}
	return info
}
func (s *KeyStore) packetDetailLocked(p storedPacket, token string) PacketDetail {
	out := PacketDetail{PacketInfo: s.packetInfoLocked(p), ClaimToken: token, Keys: []KeyInfo{}}
	for _, k := range s.keys {
		if k.Quota != nil && k.Quota.PacketID == p.ID {
			info := k.KeyInfo
			q := *info.Quota
			info.Quota = &q
			info.AllowedModels = append([]string(nil), info.AllowedModels...)
			out.Keys = append(out.Keys, info)
		}
	}
	return out
}
func (s *KeyStore) Packets() []PacketInfo {
	out := []PacketInfo{}
	if s == nil {
		return out
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := len(s.packets) - 1; i >= 0; i-- {
		out = append(out, s.packetInfoLocked(s.packets[i]))
	}
	return out
}
func (s *KeyStore) Packet(id string) (PacketDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.packets {
		if p.ID == id {
			token, err := s.decryptSecret(storedKey{KeyInfo: KeyInfo{ID: p.ID}, Hash: p.TokenHash, EncryptedSecret: p.EncryptedToken})
			if err != nil {
				return PacketDetail{}, err
			}
			return s.packetDetailLocked(p, token), nil
		}
	}
	return PacketDetail{}, ErrPacketNotFound
}
func (s *KeyStore) RevokePacket(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	packets := append([]storedPacket{}, s.packets...)
	for i := range packets {
		if packets[i].ID == id {
			if packets[i].RevokedAt != nil {
				return nil
			}
			now := time.Now().UTC()
			packets[i].RevokedAt = &now
			keys := []storedKey{}
			for _, k := range s.keys {
				if k.Quota == nil || k.Quota.PacketID != id {
					keys = append(keys, k)
				}
			}
			return s.saveState(keys, packets)
		}
	}
	return ErrPacketNotFound
}
func tokenDigest(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}
func (s *KeyStore) PublicPacket(token string) (PacketInfo, error) {
	if s == nil || len(token) != 64 {
		return PacketInfo{}, ErrPacketNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	hash := tokenDigest(token)
	for _, p := range s.packets {
		if p.TokenHash == hash {
			info := s.packetInfoLocked(p)
			if p.RevokedAt != nil || !time.Now().Before(p.ExpiresAt) {
				return PacketInfo{}, ErrPacketClosed
			}
			return info, nil
		}
	}
	return PacketInfo{}, ErrPacketNotFound
}
func (s *KeyStore) ClaimPacket(token, ip string) (PacketClaim, error) {
	if s == nil || len(token) != 64 || ip == "" {
		return PacketClaim{}, ErrPacketNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for pi, p := range s.packets {
		if p.TokenHash != tokenDigest(token) {
			continue
		}
		if p.RevokedAt != nil || !time.Now().Before(p.ExpiresAt) {
			return PacketClaim{}, ErrPacketClosed
		}
		identity := tokenDigest(p.TokenHash + ":" + ip)
		existing := p.Claims[identity]
		keys := cloneKeys(s.keys)
		for i := range keys {
			k := &keys[i]
			if k.Quota == nil || k.Quota.PacketID != p.ID || !k.Enabled {
				continue
			}
			if existing != "" {
				if k.ID != existing {
					continue
				}
			} else if k.Quota.Claimed {
				continue
			}
			secret, err := s.decryptSecret(*k)
			if err != nil {
				return PacketClaim{}, err
			}
			if existing == "" {
				k.Quota.Claimed = true
				claims := map[string]string{}
				for a, b := range p.Claims {
					claims[a] = b
				}
				claims[identity] = k.ID
				p.Claims = claims
				packets := append([]storedPacket{}, s.packets...)
				packets[pi] = p
				if err := s.saveState(keys, packets); err != nil {
					return PacketClaim{}, err
				}
			}
			info := k.KeyInfo
			quota := *info.Quota
			info.Quota = &quota
			info.AllowedModels = append([]string(nil), info.AllowedModels...)
			return PacketClaim{Key: info, Secret: secret, Repeated: existing != ""}, nil
		}
		return PacketClaim{}, ErrPacketClosed
	}
	return PacketClaim{}, ErrPacketNotFound
}

// QuotaLease tracks one request's usage. Concurrent requests settle independently
// under the store lock, independently of request archives and usage retention.
type QuotaLease struct {
	store                  *KeyStore
	id, kind               string
	units                  int64
	outstanding, uncertain bool
	finished               bool
}

func (s *KeyStore) BeginQuota(id string) (*QuotaLease, error) {
	if s == nil || id == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := cloneKeys(s.keys)
	for i := range keys {
		k := &keys[i]
		if k.ID != id {
			continue
		}
		if k.Quota == nil {
			return nil, nil
		}
		q := k.Quota
		if !keyActive(*k) || !time.Now().Before(q.ExpiresAt) {
			return nil, ErrPacketClosed
		}
		if q.Uncertain {
			return nil, ErrQuotaUncertain
		}
		if q.Used >= q.Limit {
			return nil, ErrQuotaExhausted
		}
		if !q.Pending {
			q.Pending = true
			if err := s.save(keys); err != nil {
				return nil, err
			}
		}
		if s.quotaInFlight == nil {
			s.quotaInFlight = make(map[string]int)
		}
		s.quotaInFlight[id]++
		return &QuotaLease{store: s, id: id, kind: q.Kind}, nil
	}
	return nil, ErrKeyNotFound
}
func (l *QuotaLease) StartAttempt() {
	if l != nil {
		if l.outstanding {
			l.uncertain = true
		}
		l.outstanding = true
	}
}
func (l *QuotaLease) RejectAttempt() {
	if l != nil {
		l.outstanding = false
	}
}
func (l *QuotaLease) MarkUncertain() {
	if l != nil {
		l.uncertain = true
	}
}
func (l *QuotaLease) Observe(tokens int64, hasTokens bool, credit float64, hasCredit bool) {
	if l == nil {
		return
	}
	l.outstanding = false
	var amount int64
	if l.kind == "tokens" {
		if !hasTokens || tokens < 0 {
			l.uncertain = true
			return
		}
		amount = tokens
	} else {
		if !hasCredit || math.IsNaN(credit) || math.IsInf(credit, 0) || credit < 0 || credit > 1e9 {
			l.uncertain = true
			return
		}
		amount = int64(math.Ceil(credit*float64(CreditScale) - 1e-8))
	}
	if amount > math.MaxInt64-l.units {
		l.uncertain = true
		return
	}
	l.units += amount
}
func (l *QuotaLease) Finish() (err error) {
	if l == nil {
		return nil
	}
	s := l.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.finished {
		return nil
	}
	l.finished = true
	remaining := s.quotaInFlight[l.id] - 1
	if remaining > 0 {
		s.quotaInFlight[l.id] = remaining
	} else {
		delete(s.quotaInFlight, l.id)
	}
	pending := remaining > 0
	defer func() {
		if err != nil {
			// Keep the durable marker for restart recovery and pause new requests.
			// Other in-flight requests can still settle before manual reconciliation.
			for i := range s.keys {
				if s.keys[i].ID == l.id {
					s.keys[i].Quota.Pending = pending
					s.keys[i].Quota.Uncertain = true
					break
				}
			}
		}
	}()
	keys := cloneKeys(s.keys)
	packets := append([]storedPacket{}, s.packets...)
	for i := range keys {
		if keys[i].ID == l.id {
			q := keys[i].Quota
			if l.units > math.MaxInt64-q.Used {
				return errors.New("quota overflow")
			}
			q.Used += l.units
			q.Pending = pending
			q.Uncertain = q.Uncertain || l.uncertain || l.outstanding
			for j := range packets {
				if packets[j].ID == q.PacketID {
					packets[j].UsedUnits += l.units
				}
			}
			return s.saveState(keys, packets)
		}
	}
	// A concurrent revocation deletes credentials; retain already billed usage in the packet.
	for j := range packets {
		for _, id := range packets[j].Claims {
			if id == l.id {
				packets[j].UsedUnits += l.units
				return s.saveState(keys, packets)
			}
		}
	}
	return nil
}

func (s *KeyStore) ExportPacket(id string) ([]PacketClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, p := range s.packets {
		if p.ID == id {
			found = true
			break
		}
	}
	if !found {
		return nil, ErrPacketNotFound
	}
	out := []PacketClaim{}
	for _, k := range s.keys {
		if k.Quota != nil && k.Quota.PacketID == id {
			secret, err := s.decryptSecret(k)
			if err != nil {
				return nil, err
			}
			info := k.KeyInfo
			q := *info.Quota
			info.Quota = &q
			info.AllowedModels = append([]string(nil), info.AllowedModels...)
			out = append(out, PacketClaim{Key: info, Secret: secret})
		}
	}
	return out, nil
}
func (s *KeyStore) ReconcileQuota(packetID, keyID string, used float64) error {
	if math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > 1e9 {
		return ErrPacketInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := cloneKeys(s.keys)
	packets := append([]storedPacket{}, s.packets...)
	for i := range keys {
		if keys[i].ID == keyID && keys[i].Quota != nil && keys[i].Quota.PacketID == packetID {
			q := keys[i].Quota
			if q.Pending {
				return ErrQuotaBusy
			}
			scaled := used * float64(packetScale(q.Kind))
			units := int64(math.Round(scaled))
			if math.Abs(scaled-float64(units)) > 0.000001 {
				return ErrPacketInput
			}
			delta := units - q.Used
			q.Used = units
			q.Uncertain = false
			for j := range packets {
				if packets[j].ID == packetID {
					packets[j].UsedUnits += delta
				}
			}
			return s.saveState(keys, packets)
		}
	}
	return ErrKeyNotFound
}
