package pool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

func TestRemoveZeroCreditsLargePoolPersistsOnce(t *testing.T) {
	dir := t.TempDir()
	p := New("")
	p.stateFp = filepath.Join(dir, "state.json")
	store := &removalStore{}
	p.SetStore(store)
	for i := 0; i < 3305; i++ {
		uid := fmt.Sprintf("account-%04d", i)
		a := &auth.Auth{UID: uid, AccessToken: "at", FilePath: filepath.Join(dir, uid+".json")}
		if err := a.SaveAtomic(); err != nil {
			t.Fatal(err)
		}
		p.Add(a)
		if i < 526 {
			p.SetCredits(uid, 0, int64(i%2)*100) // 真实 0/0 余额也可删除。
			switch i % 4 {
			case 1:
				p.Disable(uid, "restricted")
			case 2:
				p.Pause(uid)
			case 3:
				p.Cooldown(uid, CoolHard, time.Hour, "quota")
			}
		} else if i < 3304 {
			p.SetCredits(uid, 10, 100)
		}
	}
	// 确认前的零余额已更新为正数，删除时应以最新余额为准。
	p.SetCredits("account-0526", 0, 100)
	p.ReenableIfCredits("account-0526", 50, 100)
	p.Flush()
	store.writes = 0
	removed, failed := p.RemoveZeroCredits()
	if removed != 526 || len(failed) != 0 || store.writes != 1 {
		t.Fatalf("removed=%d failed=%v writes=%d", removed, failed, store.writes)
	}
	for i := 0; i < 3305; i++ {
		uid := fmt.Sprintf("account-%04d", i)
		_, retained := p.Status(uid)
		_, err := os.Stat(filepath.Join(dir, uid+".json"))
		if i < 526 {
			if retained || !os.IsNotExist(err) {
				t.Fatalf("zero-credit account retained: %s file err=%v", uid, err)
			}
		} else if !retained || err != nil {
			t.Fatalf("positive/unknown account deleted: %s file err=%v", uid, err)
		}
	}
	reloaded := New(p.stateFp)
	defer reloaded.Close()
	if got := len(reloaded.List()); got != 2779 {
		t.Fatalf("persisted accounts=%d, want 2779", got)
	}
	if st, _ := reloaded.Status("account-0526"); !st.CreditsKnown || st.Credits != 50 {
		t.Fatalf("refreshed balance lost: %+v", st)
	}
	if st, _ := reloaded.Status("account-3304"); st.CreditsKnown {
		t.Fatal("unknown balance became known after restart")
	}
	if removed, failed := p.RemoveZeroCredits(); removed != 0 || len(failed) != 0 || store.writes != 1 {
		t.Fatalf("empty batch removed=%d failed=%v writes=%d", removed, failed, store.writes)
	}
}

func TestZeroCreditsRequiresKnownBalance(t *testing.T) {
	p := New("")
	for _, uid := range []string{"unknown", "usage-only", "set", "snapshot", "reenable", "negative-set", "negative-snapshot", "negative-reenable"} {
		p.Add(&auth.Auth{UID: uid})
	}
	p.NoteModelCost("usage-only", "model", 100, 1000)
	p.SetCredits("set", 0, 0)
	p.SetCreditsSnapshot("snapshot", CreditsSnapshot{Remain: 0, Total: 0})
	p.ReenableIfCredits("reenable", 0, 0)
	p.SetCredits("negative-set", -1, 100)
	p.SetCreditsSnapshot("negative-snapshot", CreditsSnapshot{Remain: -1, Total: 100})
	p.ReenableIfCredits("negative-reenable", -1, 100)
	removed, failed := p.RemoveZeroCredits()
	if removed != 3 || len(failed) != 0 {
		t.Fatalf("removed=%d failed=%v", removed, failed)
	}
	for _, st := range p.List() {
		if st.CreditsKnown {
			t.Fatalf("unobserved/invalid balance is known: %+v", st)
		}
	}
}

func TestCreditsKnownRestoration(t *testing.T) {
	// 本地和 Redis 共用 applyAccountsLocked；缺字段时仅推断有余额证据的旧记录。
	const raw = `{"accounts":{
		"old-unknown":{"credits":0},
		"old-zero":{"credits":0,"credits_total":100},
		"old-positive":{"credits":50},
		"new-zero":{"credits":0,"credits_known":true},
		"new-unknown":{"credits":0,"credits_total":100,"credits_known":false}
	}}`
	var sf stateFile
	if err := json.Unmarshal([]byte(raw), &sf); err != nil {
		t.Fatal(err)
	}
	p := New("")
	p.applyAccountsLocked(sf.Accounts)
	for uid, want := range map[string]bool{"old-unknown": false, "old-zero": true, "old-positive": true, "new-zero": true, "new-unknown": false} {
		st, _ := p.Status(uid)
		if st.CreditsKnown != want {
			t.Fatalf("%s known=%v, want %v", uid, st.CreditsKnown, want)
		}
	}
	encoded, err := json.Marshal(p.stateOverviewLocked())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &sf); err != nil {
		t.Fatal(err)
	}
	reloaded := New("")
	reloaded.applyAccountsLocked(sf.Accounts)
	if removed, failed := reloaded.RemoveZeroCredits(); removed != 2 || len(failed) != 0 {
		t.Fatalf("round-trip removal=%d failed=%v", removed, failed)
	}
	if st, ok := reloaded.Status("new-unknown"); !ok || st.CreditsKnown {
		t.Fatalf("explicit unknown lost after round trip: %+v ok=%v", st, ok)
	}
}
