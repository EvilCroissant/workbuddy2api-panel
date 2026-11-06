package httpauth

import (
	"testing"
)

func TestQuotaLegacyUncertainKeyResumesWithHistory(t *testing.T) {
	s, path := packetStore(t)
	in := packetInput()
	in.Shares = 1
	p, err := s.CreatePacket(in)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := s.ClaimPacket(p.ClaimToken, "ip")
	keys := cloneKeys(s.keys)
	keys[0].Quota.Uncertain = true
	keys[0].Quota.Used = 9
	packets := append([]storedPacket{}, s.packets...)
	packets[0].UsedUnits = 9
	if err := s.saveState(keys, packets); err != nil {
		t.Fatal(err)
	}
	s, err = OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.BeginQuota(c.Key.ID)
	if err != nil {
		t.Fatalf("legacy warning still blocks key: %v", err)
	}
	lease.StartAttempt()
	lease.Observe(3, true, 0, false)
	if err := lease.Finish(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := s.Packet(p.ID)
	if q := d.Keys[0].Quota; q.Used != 12 || d.Used != 12 || !q.Uncertain || q.UnsettledRequests != 1 || q.Pending {
		t.Fatalf("legacy migration lost accounting history: %+v", d)
	}
}

func TestQuotaEstimatesPersistAndManualAdjustmentClearsMetadata(t *testing.T) {
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
	lease.ObserveEstimatedTokens(10)
	lease.StartAttempt()
	lease.Observe(4, true, 0, false)
	if err := lease.Finish(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := s.Packet(p.ID)
	if q := d.Keys[0].Quota; q.Used != 14 || q.Estimated != 10 || q.Uncertain || d.Used != 14 {
		t.Fatalf("estimated and measured usage not separated: %+v", d)
	}
	if err := s.ReconcileQuota(p.ID, c.Key.ID, 12); err != nil {
		t.Fatal(err)
	}
	d, _ = s.Packet(p.ID)
	if q := d.Keys[0].Quota; q.Used != 12 || d.Used != 12 || q.Estimated != 0 || q.UnsettledRequests != 0 || q.Uncertain {
		t.Fatalf("manual adjustment did not clear metadata: %+v", d)
	}
}
