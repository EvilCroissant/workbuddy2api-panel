package httpauth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func packetStore(t *testing.T) (*KeyStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys.json")
	s, e := OpenKeys(path)
	if e != nil {
		t.Fatal(e)
	}
	return s, path
}
func packetInput() PacketInput {
	return PacketInput{Title: "新朋友", Kind: "tokens", Mode: "random", Total: 1000, Shares: 10, Days: 7, AllowedModels: []string{"glm-5.2"}}
}
func TestPacketDistributionAndValidation(t *testing.T) {
	for _, mode := range []string{"random", "equal"} {
		for n := 1; n < 100; n++ {
			amounts, err := packetAmounts(int64(n*11+3), n, mode)
			if err != nil {
				t.Fatal(err)
			}
			var sum, min, max int64
			min = 1 << 60
			for _, x := range amounts {
				if x < 1 {
					t.Fatal("empty share")
				}
				sum += x
				if x < min {
					min = x
				}
				if x > max {
					max = x
				}
			}
			if sum != int64(n*11+3) {
				t.Fatal("allocation lost units")
			}
			if mode == "equal" && max-min > 1 {
				t.Fatalf("uneven shares %v", amounts)
			}
		}
	}
	s, _ := packetStore(t)
	for _, mutate := range []func(*PacketInput){func(p *PacketInput) { p.AllowedModels = nil }, func(p *PacketInput) { p.Total = .5 }, func(p *PacketInput) { p.Shares = 1001 }, func(p *PacketInput) { p.Days = 0 }, func(p *PacketInput) { p.Mode = "other" }, func(p *PacketInput) { p.Kind = "credit"; p.Total = .0000001 }} {
		p := packetInput()
		mutate(&p)
		if _, err := s.CreatePacket(p); err == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
	if len(s.List()) != 0 || len(s.Packets()) != 0 {
		t.Fatal("invalid create mutated store")
	}
}
func TestPacketLifecyclePersistenceAndIsolation(t *testing.T) {
	s, path := packetStore(t)
	normal, normalSecret, err := s.Create("existing")
	if err != nil {
		t.Fatal(err)
	}
	input := packetInput()
	input.Kind = "credit"
	input.Total = 10.000001
	input.Shares = 3
	input.Mode = "equal"
	p, err := s.CreatePacket(input)
	if err != nil {
		t.Fatal(err)
	}
	exported, err := s.ExportPacket(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, k := range exported {
		total += k.Key.Quota.Limit
		if VerifyAPI(req("Bearer "+k.Secret), "admin", s) {
			t.Fatal("unclaimed key active")
		}
	}
	if total != 10000001 {
		t.Fatalf("lost fractional credits %d", total)
	}
	claim, err := s.ClaimPacket(p.ClaimToken, "203.0.113.1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.ClaimPacket(p.ClaimToken, "203.0.113.1")
	if err != nil || !again.Repeated || again.Secret != claim.Secret {
		t.Fatal("duplicate IP drew another key")
	}
	if !VerifyAPI(req("Bearer "+claim.Secret), "admin", s) || VerifyAdmin(req("Bearer "+claim.Secret), "admin", s) {
		t.Fatal("bad credential permissions")
	}
	if err := s.Update(claim.Key.ID, nil, &[]string{}); err != nil {
		t.Fatal(err)
	} // credit packets may use all models
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"version": 2`) || strings.Contains(string(raw), claim.Secret) || strings.Contains(string(raw), p.ClaimToken) || strings.Contains(string(raw), "203.0.113.1") {
		t.Fatal("bad disk format or leaked secret/IP")
	}
	s, err = OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyAPI(req("Bearer "+normalSecret), "admin", s) {
		t.Fatal("legacy key lost during migration")
	}
	reloaded, err := s.Packet(p.ID)
	if err != nil || reloaded.ClaimToken != p.ClaimToken || reloaded.Claimed != 1 || reloaded.Remaining != 2 {
		t.Fatalf("reload %+v %v", reloaded, err)
	}
	lease, err := s.BeginQuota(claim.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease.StartAttempt()
	lease.Observe(100, true, 1.234567, true)
	if err = lease.Finish(); err != nil {
		t.Fatal(err)
	}
	if err = s.RevokePacket(p.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.RevokePacket(p.ID); err != nil {
		t.Fatal(err)
	}
	if VerifyAPI(req("Bearer "+claim.Secret), "admin", s) || len(s.List()) != 1 || s.List()[0].ID != normal.ID {
		t.Fatal("revocation failed or removed unrelated key")
	}
	s, err = OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err = s.Packet(p.ID)
	if err != nil || reloaded.RevokedAt == nil || reloaded.Used != 1.234567 {
		t.Fatalf("revoked usage lost %+v %v", reloaded, err)
	}
	if _, err = s.ClaimPacket(p.ClaimToken, "203.0.113.2"); !errors.Is(err, ErrPacketClosed) {
		t.Fatal(err)
	}
}
func TestConcurrentPacketClaims(t *testing.T) {
	s, _ := packetStore(t)
	p, err := s.CreatePacket(packetInput())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ids := make(chan string, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := s.ClaimPacket(p.ClaimToken, "same-ip")
			if err != nil {
				t.Error(err)
				return
			}
			ids <- c.Key.ID
		}()
	}
	wg.Wait()
	close(ids)
	one := ""
	for id := range ids {
		if one != "" && one != id {
			t.Fatal("same IP drew multiple keys")
		}
		one = id
	}
	var successes int
	var mu sync.Mutex
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.ClaimPacket(p.ClaimToken, string(rune('A'+i)))
			if err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			} else if !errors.Is(err, ErrPacketClosed) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if successes != 9 {
		t.Fatalf("claim count=%d", successes)
	}
	got, _ := s.PublicPacket(p.ClaimToken)
	if got.Claimed != 10 || got.Remaining != 0 {
		t.Fatalf("claims %+v", got)
	}
}
func TestQuotaEnforcementAndRecovery(t *testing.T) {
	s, path := packetStore(t)
	in := packetInput()
	in.Total = 20
	in.Shares = 1
	p, err := s.CreatePacket(in)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := s.ClaimPacket(p.ClaimToken, "ip")
	if err := s.Update(c.Key.ID, nil, &[]string{}); !errors.Is(err, ErrPacketModels) {
		t.Fatal("token model limits cleared")
	}
	lease, err := s.BeginQuota(c.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease.StartAttempt()
	lease.RejectAttempt()
	lease.StartAttempt()
	lease.Observe(12, true, 0, false)
	if err = lease.Finish(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	lease, err = s.BeginQuota(c.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease.StartAttempt()
	lease.Observe(10, true, 0, false)
	if err = lease.Finish(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginQuota(c.Key.ID); !errors.Is(err, ErrQuotaExhausted) {
		t.Fatal("exhausted key accepted")
	}
	if err = s.ReconcileQuota(p.ID, c.Key.ID, 1); err != nil {
		t.Fatal(err)
	}
	lease, err = s.BeginQuota(c.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease.StartAttempt()
	lease.Observe(0, false, 0, false)
	if err = lease.Finish(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginQuota(c.Key.ID); !errors.Is(err, ErrQuotaUncertain) {
		t.Fatal("missing usage silently free")
	}
	if err = s.ReconcileQuota(p.ID, c.Key.ID, 5); err != nil {
		t.Fatal(err)
	}
	lease, err = s.BeginQuota(c.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash with durable Pending=true.
	restarted, err := OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.BeginQuota(c.Key.ID); !errors.Is(err, ErrQuotaUncertain) {
		t.Fatal("crash reset billing reservation")
	}
	if err = lease.Finish(); err != nil {
		t.Fatal(err)
	}
	// Expiry applies even though credentials have been claimed.
	s.mu.Lock()
	keys := cloneKeys(s.keys)
	for i := range keys {
		if keys[i].ID == c.Key.ID {
			keys[i].Quota.ExpiresAt = time.Now().Add(-time.Hour)
		}
	}
	err = s.save(keys)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginQuota(c.Key.ID); !errors.Is(err, ErrPacketClosed) {
		t.Fatal("expired quota accepted")
	}
}

func TestQuotaConcurrentRequestsSettleIndependently(t *testing.T) {
	for _, kind := range []string{"tokens", "credit"} {
		t.Run(kind, func(t *testing.T) {
			s, path := packetStore(t)
			in := packetInput()
			in.Kind, in.Total, in.Shares = kind, 10, 1
			p, err := s.CreatePacket(in)
			if err != nil {
				t.Fatal(err)
			}
			c, err := s.ClaimPacket(p.ClaimToken, "ip")
			if err != nil {
				t.Fatal(err)
			}
			const requests = 8
			leases := make([]*QuotaLease, requests)
			var wg sync.WaitGroup
			for i := range leases {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					var err error
					leases[i], err = s.BeginQuota(c.Key.ID)
					if err != nil {
						t.Error(err)
					}
				}(i)
			}
			wg.Wait()
			if t.Failed() {
				t.FailNow()
			}
			settle := func(l *QuotaLease) error {
				l.StartAttempt()
				l.Observe(6, true, 6, true)
				return l.Finish()
			}
			if err := settle(leases[0]); err != nil {
				t.Fatal(err)
			}
			after, _ := s.Packet(p.ID)
			if after.Used != 6 || !after.Keys[0].Quota.Pending {
				t.Fatalf("partial settlement lost pending requests: %+v", after)
			}
			if err := s.ReconcileQuota(p.ID, c.Key.ID, 0); !errors.Is(err, ErrQuotaBusy) {
				t.Fatalf("reconciliation accepted during active requests: %v", err)
			}
			restarted, err := OpenKeys(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := restarted.BeginQuota(c.Key.ID); !errors.Is(err, ErrQuotaUncertain) {
				t.Fatalf("restart lost unfinished requests: %v", err)
			}
			if err := settle(leases[1]); err != nil {
				t.Fatal(err)
			}
			if _, err := s.BeginQuota(c.Key.ID); !errors.Is(err, ErrQuotaExhausted) {
				t.Fatalf("new request accepted after quota exhaustion: %v", err)
			}
			for _, l := range leases[2:] {
				wg.Add(1)
				go func(l *QuotaLease) {
					defer wg.Done()
					if err := settle(l); err != nil {
						t.Error(err)
					}
				}(l)
			}
			wg.Wait()
			if err := leases[0].Finish(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenKeys(path)
			if err != nil {
				t.Fatal(err)
			}
			after, _ = reopened.Packet(p.ID)
			q := after.Keys[0].Quota
			if after.Used != 6*requests || q.Used != int64(6*requests)*packetScale(kind) || q.Pending || q.Uncertain {
				t.Fatalf("concurrent usage lost or counted twice: %+v quota=%+v", after, q)
			}
		})
	}
}

func TestQuotaConcurrentSettlementFailureKeepsOtherRequestsPending(t *testing.T) {
	for _, failure := range []string{"missing_usage", "write_failure"} {
		t.Run(failure, func(t *testing.T) {
			s, path := packetStore(t)
			in := packetInput()
			in.Shares = 1
			p, err := s.CreatePacket(in)
			if err != nil {
				t.Fatal(err)
			}
			c, _ := s.ClaimPacket(p.ClaimToken, "ip")
			first, err := s.BeginQuota(c.Key.ID)
			if err != nil {
				t.Fatal(err)
			}
			second, err := s.BeginQuota(c.Key.ID)
			if err != nil {
				t.Fatal(err)
			}
			first.StartAttempt()
			if failure == "write_failure" {
				first.Observe(17, true, 0, false)
				s.path = filepath.Join(path, "not-a-directory")
			}
			if err := first.Finish(); (err != nil) != (failure == "write_failure") {
				t.Fatalf("unexpected settlement error: %v", err)
			}
			s.path = path
			after, _ := s.Packet(p.ID)
			if !after.Keys[0].Quota.Pending || !after.Keys[0].Quota.Uncertain {
				t.Fatal("failed request cleared the other active request")
			}
			if _, err := s.BeginQuota(c.Key.ID); !errors.Is(err, ErrQuotaUncertain) {
				t.Fatalf("new request accepted with uncertain usage: %v", err)
			}
			if err := s.ReconcileQuota(p.ID, c.Key.ID, 17); !errors.Is(err, ErrQuotaBusy) {
				t.Fatalf("reconciliation accepted before remaining request finished: %v", err)
			}
			second.StartAttempt()
			second.Observe(23, true, 0, false)
			if err := second.Finish(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenKeys(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reopened.BeginQuota(c.Key.ID); !errors.Is(err, ErrQuotaUncertain) {
				t.Fatalf("later settlement erased uncertain usage: %v", err)
			}
			if err := s.ReconcileQuota(p.ID, c.Key.ID, 40); err != nil {
				t.Fatal(err)
			}
			after, _ = s.Packet(p.ID)
			if after.Used != 40 || after.Keys[0].Quota.Pending || after.Keys[0].Quota.Uncertain {
				t.Fatalf("concurrent reconciliation failed: %+v", after)
			}
		})
	}
}

func TestFailedPacketWriteIsAtomic(t *testing.T) {
	s, path := packetStore(t)
	_, _, err := s.Create("normal")
	if err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	s.path = filepath.Join(path, "unwritable.json")
	if _, err = s.CreatePacket(packetInput()); err == nil {
		t.Fatal("write failure hidden")
	}
	if len(s.List()) != 1 || len(s.Packets()) != 0 {
		t.Fatal("partially created batch")
	}
	var decoded any
	if err = json.Unmarshal(original, &decoded); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaFailedSettlementCanBeReconciled(t *testing.T) {
	s, path := packetStore(t)
	p, err := s.CreatePacket(packetInput())
	if err != nil {
		t.Fatal(err)
	}
	c, _ := s.ClaimPacket(p.ClaimToken, "ip")
	lease, err := s.BeginQuota(c.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease.StartAttempt()
	lease.Observe(17, true, 0, false)
	s.path = filepath.Join(path, "not-a-directory")
	if err = lease.Finish(); err == nil {
		t.Fatal("settlement failure hidden")
	}
	s.path = path
	if _, err = s.BeginQuota(c.Key.ID); !errors.Is(err, ErrQuotaUncertain) {
		t.Fatal("failed settlement allowed more usage")
	}
	restarted, err := OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.BeginQuota(c.Key.ID); !errors.Is(err, ErrQuotaUncertain) {
		t.Fatal("pending reservation lost")
	}
	if err = s.ReconcileQuota(p.ID, c.Key.ID, 17); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Packet(p.ID)
	if after.Used != 17 || after.Keys[0].Quota.Uncertain || after.Keys[0].Quota.Pending {
		t.Fatalf("bad reconciliation %+v", after)
	}
}
func TestPacketRevocationDuringRequestRetainsCharge(t *testing.T) {
	s, _ := packetStore(t)
	p, err := s.CreatePacket(packetInput())
	if err != nil {
		t.Fatal(err)
	}
	c, _ := s.ClaimPacket(p.ClaimToken, "ip")
	lease, err := s.BeginQuota(c.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease.StartAttempt()
	if err = s.RevokePacket(p.ID); err != nil {
		t.Fatal(err)
	}
	lease.Observe(37, true, 0, false)
	if err = lease.Finish(); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Packet(p.ID)
	if after.Used != 37 || len(after.Keys) != 0 || after.RevokedAt == nil {
		t.Fatalf("in-flight usage lost %+v", after)
	}
}

func TestPacketThousandShares(t *testing.T) {
	s, _ := packetStore(t)
	in := packetInput()
	in.Shares = 1000
	in.Total = 1000000
	p, err := s.CreatePacket(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Keys) != 1000 || p.Remaining != 1000 {
		t.Fatalf("incomplete batch keys=%d remaining=%d", len(p.Keys), p.Remaining)
	}
	var units int64
	for _, k := range p.Keys {
		units += k.Quota.Limit
	}
	if units != 1000000 {
		t.Fatalf("wrong total %d", units)
	}
	c, err := s.ClaimPacket(p.ClaimToken, "ip")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.BeginQuota(c.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease.StartAttempt()
	lease.Observe(42, true, 0, false)
	if err = lease.Finish(); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Packet(p.ID)
	if after.Used != 42 || after.Remaining != 999 {
		t.Fatalf("incorrect large batch accounting %+v", after.PacketInfo)
	}
}
