package httpauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestPacketAllocatesOnlyWhenClaimed(t *testing.T) {
	for _, mode := range []string{"random", "equal"} {
		for _, tc := range []struct {
			kind   string
			units  int64
			shares int
		}{
			{"tokens", 11, 3}, {"credit", 10000001, 3},
			{"tokens", 5, 5}, {"credit", 5, 5},
			{"tokens", 7, 1}, {"credit", 7, 1},
			{"tokens", 1000000000, 7}, {"credit", 1000000000000000, 7},
		} {
			t.Run(fmt.Sprintf("%s/%s/%d/%d", mode, tc.kind, tc.units, tc.shares), func(t *testing.T) {
				s, path := packetStore(t)
				in := packetInput()
				in.Mode, in.Kind, in.Shares = mode, tc.kind, tc.shares
				in.Total = float64(tc.units) / float64(packetScale(tc.kind))
				p, err := s.CreatePacket(in)
				if err != nil {
					t.Fatal(err)
				}
				if len(p.Keys) != 0 || len(s.List()) != 0 || p.Claimed != 0 || p.Remaining != tc.shares {
					t.Fatalf("creation preallocated keys: %+v", p)
				}
				// An empty key array and allocation counters must survive restart.
				s, err = OpenKeys(path)
				if err != nil {
					t.Fatal(err)
				}
				var sum int64
				for i := 0; i < tc.shares; i++ {
					info, err := s.PublicPacket(p.ClaimToken)
					if err != nil || info.Remaining != tc.shares-i || info.Claimed != i {
						t.Fatalf("incorrect available shares: %+v %v", info, err)
					}
					claim, err := s.ClaimPacket(p.ClaimToken, fmt.Sprintf("ip-%d", i))
					if err != nil {
						t.Fatal(err)
					}
					q := claim.Key.Quota
					if claim.Repeated || q.Limit < 1 || !q.Claimed || q.Kind != tc.kind || !q.ExpiresAt.Equal(p.ExpiresAt) {
						t.Fatalf("invalid claim: %+v", claim)
					}
					if !VerifyAPI(req("Bearer "+claim.Secret), "admin", s) || !s.AllowsModel(claim.Key.ID, "cn", "glm-5.2") || s.AllowsModel(claim.Key.ID, "global", "glm-5.2") {
						t.Fatal("claim lost key permissions")
					}
					sum += q.Limit
					s, err = OpenKeys(path)
					if err != nil {
						t.Fatal(err)
					}
					again, err := s.ClaimPacket(p.ClaimToken, fmt.Sprintf("ip-%d", i))
					if err != nil || !again.Repeated || again.Secret != claim.Secret || again.Key.Quota.Limit != q.Limit {
						t.Fatalf("restart changed a claim: %+v %v", again, err)
					}
					if len(s.List()) != i+1 || s.packets[0].Allocation.AssignedUnits != sum {
						t.Fatal("claim allocation not persisted exactly once")
					}
				}
				if sum != tc.units {
					t.Fatalf("total allocation=%d want=%d", sum, tc.units)
				}
				if _, err := s.ClaimPacket(p.ClaimToken, "extra-ip"); !errors.Is(err, ErrPacketClosed) {
					t.Fatalf("extra share accepted: %v", err)
				}
			})
		}
	}
}

func TestFailedPacketClaimDoesNotAllocate(t *testing.T) {
	s, path := packetStore(t)
	p, err := s.CreatePacket(packetInput())
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(path, "not-a-directory")
	if _, err := s.ClaimPacket(p.ClaimToken, "ip"); err == nil {
		t.Fatal("claim hid write failure")
	}
	info, err := s.Packet(p.ID)
	if err != nil || len(info.Keys) != 0 || info.Claimed != 0 || info.Remaining != p.Shares || s.packets[0].Allocation.AssignedUnits != 0 {
		t.Fatalf("failed claim consumed a share: %+v %v", info, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("failed claim modified durable state")
	}
	s, err = OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimPacket(p.ClaimToken, "ip")
	if err != nil || claim.Repeated || len(s.List()) != 1 {
		t.Fatalf("failed claim could not be retried: %+v %v", claim, err)
	}
}

func TestAssignedPacketQuotaNeverReturnsToPool(t *testing.T) {
	for _, action := range []string{"consume", "reconcile", "delete", "disable"} {
		t.Run(action, func(t *testing.T) {
			s, path := packetStore(t)
			in := packetInput()
			in.Total, in.Shares, in.Mode = 10, 3, "equal"
			p, err := s.CreatePacket(in)
			if err != nil {
				t.Fatal(err)
			}
			first, err := s.ClaimPacket(p.ClaimToken, "first")
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "consume":
				var lease *QuotaLease
				lease, err = s.BeginQuota(first.Key.ID)
				if err == nil {
					lease.StartAttempt()
					lease.Observe(4, true, 0, false)
					err = lease.Finish()
				}
			case "reconcile":
				err = s.ReconcileQuota(p.ID, first.Key.ID, 4)
			case "delete":
				err = s.Delete(first.Key.ID)
			case "disable":
				err = s.SetEnabled(first.Key.ID, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			s, err = OpenKeys(path)
			if err != nil {
				t.Fatal(err)
			}
			again, err := s.ClaimPacket(p.ClaimToken, "first")
			if action == "delete" || action == "disable" {
				if !errors.Is(err, ErrPacketClosed) {
					t.Fatalf("deleted/disabled claimant redrew: %+v %v", again, err)
				}
			} else if err != nil || !again.Repeated || again.Key.ID != first.Key.ID {
				t.Fatalf("usage changed existing claim: %+v %v", again, err)
			}
			second, err := s.ClaimPacket(p.ClaimToken, "second")
			if err != nil {
				t.Fatal(err)
			}
			last, err := s.ClaimPacket(p.ClaimToken, "last")
			if err != nil {
				t.Fatal(err)
			}
			if first.Key.Quota.Limit != 3 || second.Key.Quota.Limit != 3 || last.Key.Quota.Limit != 4 || s.packets[0].Allocation.AssignedUnits != 10 {
				t.Fatal("assigned quota changed after key mutation")
			}
			if _, err := s.ClaimPacket(p.ClaimToken, "extra"); !errors.Is(err, ErrPacketClosed) {
				t.Fatalf("key mutation made another share available: %v", err)
			}
		})
	}
}

func TestPacketClaimsDuringBillingKeepBothTotals(t *testing.T) {
	for _, kind := range []string{"credit", "tokens"} {
		t.Run(kind, func(t *testing.T) {
			s, path := packetStore(t)
			in := packetInput()
			in.Kind, in.Total, in.Shares = kind, 1200, 12
			p, err := s.CreatePacket(in)
			if err != nil {
				t.Fatal(err)
			}
			first, err := s.ClaimPacket(p.ClaimToken, "first")
			if err != nil {
				t.Fatal(err)
			}
			leases := make([]*QuotaLease, 4)
			for i := range leases {
				leases[i], err = s.BeginQuota(first.Key.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := 1; i < in.Shares; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					if _, err := s.ClaimPacket(p.ClaimToken, fmt.Sprintf("ip-%d", i)); err != nil {
						t.Error(err)
					}
				}(i)
			}
			for _, lease := range leases {
				wg.Add(1)
				go func(lease *QuotaLease) {
					defer wg.Done()
					<-start
					lease.StartAttempt()
					lease.Observe(1, true, 1, true)
					if err := lease.Finish(); err != nil {
						t.Error(err)
					}
				}(lease)
			}
			close(start)
			wg.Wait()
			s, err = OpenKeys(path)
			if err != nil {
				t.Fatal(err)
			}
			after, err := s.Packet(p.ID)
			if err != nil || after.Used != 4 || after.Claimed != 12 || after.Remaining != 0 || len(after.Keys) != 12 {
				t.Fatalf("concurrent claims or charges lost: %+v %v", after, err)
			}
			var assigned int64
			for _, key := range after.Keys {
				assigned += key.Quota.Limit
			}
			if assigned != 1200*packetScale(kind) || s.packets[0].Allocation.AssignedUnits != assigned {
				t.Fatal("billing changed unallocated quota")
			}
		})
	}
}

func TestLegacyPreallocatedPacketKeepsKeysAndAmounts(t *testing.T) {
	s, path := packetStore(t)
	in := packetInput()
	in.Total, in.Shares = 10, 2
	p, err := s.CreatePacket(in)
	if err != nil {
		t.Fatal(err)
	}
	legacy := s.packets[0]
	legacy.Allocation = nil
	keys := []storedKey{}
	for i, amount := range []int64{3, 7} {
		id, secret := fmt.Sprintf("legacy-%d", i), fmt.Sprintf("sk-wb-legacy-secret-%d", i)
		encrypted, err := s.encryptSecret(id, secret)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, storedKey{KeyInfo: KeyInfo{ID: id, Name: id, Prefix: secret[:14], Enabled: true, CreatedAt: p.CreatedAt, AllowedModels: p.AllowedModels,
			Quota: &KeyQuota{PacketID: p.ID, Kind: p.Kind, Limit: amount, ExpiresAt: p.ExpiresAt}}, Hash: tokenDigest(secret), EncryptedSecret: encrypted})
	}
	if err := s.saveState(keys, []storedPacket{legacy}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(raw, []byte(`"version": 2`)) {
		t.Fatal("fixture is not a v2 packet")
	}
	s, err = OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range keys {
		if VerifyAPI(req(fmt.Sprintf("Bearer sk-wb-legacy-secret-%d", i)), "admin", s) {
			t.Fatal("legacy unclaimed key active")
		}
	}
	first, err := s.ClaimPacket(p.ClaimToken, "first")
	if err != nil || first.Key.ID != "legacy-0" || first.Key.Quota.Limit != 3 || first.Secret != "sk-wb-legacy-secret-0" {
		t.Fatalf("legacy first key changed: %+v %v", first, err)
	}
	// Both packet types must coexist after a v2 -> v3 upgrade.
	newPacket, err := s.CreatePacket(packetInput())
	if err != nil {
		t.Fatal(err)
	}
	s, err = OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimPacket(p.ClaimToken, "second")
	if err != nil || second.Key.ID != "legacy-1" || second.Key.Quota.Limit != 7 || second.Secret != "sk-wb-legacy-secret-1" {
		t.Fatalf("legacy remaining key redrawn: %+v %v", second, err)
	}
	again, err := s.ClaimPacket(p.ClaimToken, "first")
	if err != nil || !again.Repeated || again.Secret != first.Secret {
		t.Fatal("legacy repeat changed")
	}
	if _, err := s.ClaimPacket(newPacket.ClaimToken, "new"); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokePacket(p.ID); err != nil || len(s.List()) != 1 {
		t.Fatalf("legacy revocation affected new packet: %v", err)
	}
}

func TestUnclaimedPacketExpiryAndRevocation(t *testing.T) {
	for _, action := range []string{"expire", "revoke"} {
		t.Run(action, func(t *testing.T) {
			s, path := packetStore(t)
			p, err := s.CreatePacket(packetInput())
			if err != nil {
				t.Fatal(err)
			}
			if action == "expire" {
				s.packets[0].ExpiresAt = time.Now().Add(-time.Hour)
				err = s.saveState(s.keys, s.packets)
			} else {
				err = s.RevokePacket(p.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			s, err = OpenKeys(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.PublicPacket(p.ClaimToken); !errors.Is(err, ErrPacketClosed) {
				t.Fatalf("closed packet remained public: %v", err)
			}
			if _, err := s.ClaimPacket(p.ClaimToken, "ip"); !errors.Is(err, ErrPacketClosed) {
				t.Fatalf("closed packet allocated key: %v", err)
			}
			info, err := s.Packet(p.ID)
			if err != nil || info.Remaining != 0 || info.Claimed != 0 || len(info.Keys) != 0 {
				t.Fatalf("closed empty packet changed: %+v %v", info, err)
			}
		})
	}
}

func TestPacketAllocationValidation(t *testing.T) {
	s, path := packetStore(t)
	if _, err := s.CreatePacket(packetInput()); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*storedPacket){
		func(p *storedPacket) { p.Allocation.TotalUnits++ },
		func(p *storedPacket) { p.Allocation.AssignedUnits = -1 },
		func(p *storedPacket) { p.Allocation.AssignedUnits = 1001 },
		func(p *storedPacket) { p.Allocation.AssignedUnits = 1 },
		func(p *storedPacket) { p.Claims["ip"] = "key" },
		func(p *storedPacket) { p.Claims["ip"] = "key"; p.Allocation.AssignedUnits = 1000 },
	} {
		var disk keyFile
		if err := json.Unmarshal(original, &disk); err != nil {
			t.Fatal(err)
		}
		mutate(&disk.Packets[0])
		raw, err := json.Marshal(disk)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenKeys(path); err == nil {
			t.Fatal("inconsistent allocation was accepted")
		}
	}
}
