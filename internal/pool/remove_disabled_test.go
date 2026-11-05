package pool

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

type removalStore struct{ writes int }

func (s *removalStore) SaveState([]byte)          { s.writes++ }
func (s *removalStore) LoadState() ([]byte, bool) { return nil, false }

func TestRemoveDisabledLargePoolPersistsOnce(t *testing.T) {
	dir := t.TempDir()
	// 不启动定时 flusher，直接观察本次批量操作的持久化次数。
	p := New("")
	p.stateFp = filepath.Join(dir, "state.json")
	store := &removalStore{}
	p.SetStore(store)
	authDir := filepath.Join(dir, "auths")
	if err := os.Mkdir(authDir, 0700); err != nil {
		t.Fatal(err)
	}
	add := func(uid string) *auth.Auth {
		t.Helper()
		a := &auth.Auth{UID: uid, AccessToken: "at", FilePath: filepath.Join(authDir, "workbuddy-"+uid+".json")}
		if err := a.SaveAtomic(); err != nil {
			t.Fatal(err)
		}
		p.Add(a)
		return a
	}
	for i := 0; i < 1183; i++ {
		a := add(fmt.Sprintf("disabled-%04d", i))
		p.Disable(a.UID, "restricted")
	}
	for _, uid := range []string{"healthy", "paused", "cooling", "revived"} {
		add(uid)
	}
	p.Pause("paused")
	p.Cooldown("cooling", CoolHard, time.Hour, "quota")
	p.Disable("revived", "manual")
	p.Revive("revived")
	p.Flush()
	store.writes = 0

	removed, failed := p.RemoveDisabled()
	if removed != 1183 || len(failed) != 0 {
		t.Fatalf("removed=%d failed=%v", removed, failed)
	}
	if store.writes != 1 {
		t.Fatalf("snapshot writes=%d, want one per batch", store.writes)
	}
	for i := 0; i < 1183; i++ {
		uid := fmt.Sprintf("disabled-%04d", i)
		if _, ok := p.Status(uid); ok {
			t.Fatalf("disabled account retained: %s", uid)
		}
		if _, err := os.Stat(filepath.Join(authDir, "workbuddy-"+uid+".json")); !os.IsNotExist(err) {
			t.Fatalf("credential retained: %s: %v", uid, err)
		}
	}
	remaining, err := auth.LoadDir(authDir)
	if err != nil || len(remaining) != 4 {
		t.Fatalf("remaining auths=%d err=%v", len(remaining), err)
	}
	reloaded := New(p.stateFp)
	defer reloaded.Close()
	if len(reloaded.List()) != 4 {
		t.Fatalf("persisted accounts=%d, want 4", len(reloaded.List()))
	}
	reloaded.SyncToDir(remaining)
	for _, uid := range []string{"healthy", "paused", "cooling", "revived"} {
		if _, ok := reloaded.Status(uid); !ok {
			t.Fatalf("unrelated account deleted: %s", uid)
		}
	}
	if st, _ := reloaded.Status("paused"); !st.Paused {
		t.Fatal("paused state lost")
	}
	if st, _ := reloaded.Status("cooling"); !st.Cooling {
		t.Fatal("cooldown state lost")
	}
	if removed, failed := p.RemoveDisabled(); removed != 0 || len(failed) != 0 {
		t.Fatalf("repeat removal=%d failed=%v", removed, failed)
	}
	if store.writes != 1 {
		t.Fatal("empty batch rewrote state")
	}
}

func TestRemoveDisabledRetainsFileFailures(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked.json")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "blocker"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	p := New("")
	for _, a := range []*auth.Auth{
		{UID: "failed", FilePath: blocked},
		{UID: "already-missing", FilePath: filepath.Join(dir, "missing.json")},
		{UID: "memory-only"},
	} {
		p.Add(a)
		p.Disable(a.UID, "restricted")
	}
	removed, failed := p.RemoveDisabled()
	if removed != 2 || len(failed) != 1 || failed["failed"] == nil {
		t.Fatalf("removed=%d failed=%v", removed, failed)
	}
	if st, ok := p.Status("failed"); !ok || !st.Disabled || st.DisabledReason != "restricted" {
		t.Fatalf("failed account state lost: %+v ok=%v", st, ok)
	}
}
