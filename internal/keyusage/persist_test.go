package keyusage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/reqlog"
)

func TestRollupAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key-usage.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 3, 12, 34, 0, 0, time.UTC)
	old := now.Add(-91 * 24 * time.Hour)
	recent := now.Add(-30 * time.Minute)

	// Record the old event relative to its own time so it initially lands in an
	// hourly bucket, then explicitly exercise the current-time rollup.
	r.recordAt(reqlog.Event{
		Time:        old,
		RequestID:   "old",
		KeyID:       "key",
		Model:       "m",
		Status:      200,
		OK:          true,
		Outcome:     reqlog.OutcomeSuccess,
		TotalTokens: 11,
		TokenKnown:  true,
		DurationMs:  20,
		Credit:      0.25,
		HasCredit:   true,
	}, old)
	r.recordAt(reqlog.Event{
		Time:        recent,
		RequestID:   "recent",
		KeyID:       "key",
		Model:       "m",
		Status:      200,
		OK:          true,
		Outcome:     reqlog.OutcomeSuccess,
		TotalTokens: 7,
		TokenKnown:  true,
		DurationMs:  40,
	}, now)

	r.mu.Lock()
	oldWasHourly := false
	for id := range r.buckets {
		if id.start.Equal(old.Truncate(time.Hour)) && id.granularity == hourGranularity {
			oldWasHourly = true
		}
	}
	r.mu.Unlock()
	if !oldWasHourly {
		t.Fatal("old event did not start in an hourly bucket")
	}

	r.rollupAt(now)
	r.mu.Lock()
	dayBuckets := 0
	for id := range r.buckets {
		if id.keyID == "key" && id.start.Equal(utcDayStart(old)) {
			if id.granularity != dayGranularity {
				t.Fatalf("old bucket granularity = %q", id.granularity)
			}
			dayBuckets++
		}
	}
	r.mu.Unlock()
	if dayBuckets != 1 {
		t.Fatalf("old day buckets = %d, want 1", dayBuckets)
	}

	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	r2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	r2.rollupAt(now)
	r2.rollupAt(now)

	all := r2.summaryAt(0, now)["key"]
	if all.Requests != 2 || all.TotalTokens != 18 || all.CreditRequests != 1 {
		t.Fatalf("all-history stats after restart = %+v", all)
	}
	month := r2.summaryAt(720, now)["key"]
	if month.Requests != 1 || month.TotalTokens != 7 {
		t.Fatalf("30-day stats = %+v", month)
	}
	snap := r2.snapshotAt("key", 0, now)
	if len(snap.Series) != 2 || len(snap.Recent) != 2 {
		t.Fatalf("all-history snapshot = series:%+v recent:%+v", snap.Series, snap.Recent)
	}
}

func TestPersistencePrivacyRestartAndCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key-usage.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Summary(0); len(got) != 0 {
		t.Fatalf("missing file should create empty recorder: %+v", got)
	}
	now := time.Date(2026, 11, 3, 12, 34, 0, 0, time.UTC)
	r.recordAt(reqlog.Event{
		Time:             now,
		RequestID:        "req-private",
		KeyID:            "key",
		Path:             "/v1/chat/completions",
		Account:          "sensitive nickname(uid12345)",
		Model:            "model",
		Status:           200,
		OK:               true,
		Outcome:          reqlog.OutcomeSuccess,
		DurationMs:       12,
		TTFBMs:           3,
		Attempts:         2,
		PromptTokens:     4,
		CompletionTokens: 6,
		TotalTokens:      10,
		TokenKnown:       true,
		Credit:           0.5,
		HasCredit:        true,
		CacheHitTokens:   2,
		CacheMissTokens:  2,
		ClientIP:         "203.0.113.9",
		UserAgent:        "private-agent/1.0",
	}, now)
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %04o, want 0600", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"sensitive nickname", "203.0.113.9", "private-agent/1.0",
		`"account"`, `"client_ip"`, `"user_agent"`, `"ttfb_ms"`,
		`"cache_hit_tokens"`, `"cache_miss_tokens"`,
	} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("persisted data contains forbidden field/value %q:\n%s", forbidden, raw)
		}
	}
	for _, required := range []string{
		`"request_id": "req-private"`, `"token_known": true`, `"credit_known": true`,
	} {
		if !bytes.Contains(raw, []byte(required)) {
			t.Fatalf("persisted data missing %q:\n%s", required, raw)
		}
	}

	r2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	snap := r2.snapshotAt("key", 24, now)
	if snap.Totals.Requests != 1 || snap.Totals.TotalTokens != 10 ||
		snap.Totals.LastUsedAt == nil || !snap.Totals.LastUsedAt.Equal(now) {
		t.Fatalf("restarted totals = %+v", snap.Totals)
	}
	if len(snap.Recent) != 1 {
		t.Fatalf("restarted recent = %+v", snap.Recent)
	}
	event := snap.Recent[0]
	if event.KeyID != "key" {
		t.Fatalf("restarted recent key_id = %q, want key", event.KeyID)
	}
	if event.Account != "" || event.ClientIP != "" || event.UserAgent != "" ||
		event.TTFBMs != 0 || event.CacheHitTokens != 0 || event.CacheMissTokens != 0 {
		t.Fatalf("recent event was not minimized: %+v", event)
	}

	corruptPath := filepath.Join(dir, "corrupt.json")
	corrupt := []byte("{not-json")
	if err := os.WriteFile(corruptPath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(corruptPath); err == nil {
		t.Fatal("Open accepted corrupt JSON")
	}
	after, err := os.ReadFile(corruptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, corrupt) {
		t.Fatalf("corrupt file was overwritten: %q", after)
	}
}

func TestSaveFailureKeepsDirtyAndRetries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key-usage.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Hour)
	r.recordAt(reqlog.Event{
		Time:    now,
		KeyID:   "key",
		Model:   "m",
		Status:  200,
		OK:      true,
		Outcome: reqlog.OutcomeSuccess,
	}, now)

	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.path = filepath.Join(blocker, "key-usage.json")
	if err := r.Save(); err == nil {
		t.Fatal("Save unexpectedly succeeded under a file parent")
	}
	r.mu.Lock()
	dirty := r.dirty
	r.mu.Unlock()
	if !dirty {
		t.Fatal("Save failure cleared dirty")
	}

	r.path = path
	if err := r.Save(); err != nil {
		t.Fatalf("retry Save: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.summaryAt(24, now)["key"]; got.Requests != 1 {
		t.Fatalf("retried Save stats = %+v", got)
	}
}

func TestBackgroundSaveLoopAndIdempotentLifecycle(t *testing.T) {
	if saveInterval != 30*time.Second {
		t.Fatalf("save interval = %v, want 30s", saveInterval)
	}
	path := filepath.Join(t.TempDir(), "key-usage.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r.recordAt(reqlog.Event{
		Time:    now,
		KeyID:   "key",
		Model:   "m",
		Status:  200,
		OK:      true,
		Outcome: reqlog.OutcomeSuccess,
	}, now)

	stop := make(chan struct{})
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.saveLoop(stop, ticks)
	}()
	select {
	case ticks <- now:
	case <-time.After(time.Second):
		t.Fatal("save loop did not accept tick")
	}
	waitForFile(t, path)
	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("save loop did not stop")
	}

	r.Start()
	r.Start()
	r.Stop()
	r.Stop()
	r.Stop()

	withoutStart, err := Open(filepath.Join(t.TempDir(), "flush.json"))
	if err != nil {
		t.Fatal(err)
	}
	withoutStart.recordAt(reqlog.Event{
		Time:    now,
		KeyID:   "key",
		Model:   "m",
		Status:  200,
		OK:      true,
		Outcome: reqlog.OutcomeSuccess,
	}, now)
	withoutStart.Stop()
	if got := withoutStart.Summary(24)["key"]; got.Requests != 1 {
		t.Fatalf("Stop without Start did not flush: %+v", got)
	}
}

func TestConcurrentRecordSaveSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key-usage.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	const writers = 8
	const perWriter = 50
	start := make(chan struct{})
	var wg sync.WaitGroup
	for writer := 0; writer < writers; writer++ {
		writer := writer
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < perWriter; i++ {
				at := base.Add(-time.Duration(writer*perWriter+i) * time.Second)
				r.recordAt(reqlog.Event{
					Time:        at,
					RequestID:   fmt.Sprintf("%d-%d", writer, i),
					KeyID:       "key",
					Model:       "m",
					Status:      200,
					OK:          true,
					Outcome:     reqlog.OutcomeSuccess,
					DurationMs:  int64(i + 1),
					TotalTokens: 1,
					TokenKnown:  true,
				}, base)
			}
		}()
	}
	var savers sync.WaitGroup
	for i := 0; i < 4; i++ {
		savers.Add(1)
		go func() {
			defer savers.Done()
			<-start
			for j := 0; j < 8; j++ {
				if err := r.Save(); err != nil {
					t.Errorf("concurrent Save: %v", err)
					return
				}
				_ = r.summaryAt(24, base)
				_ = r.snapshotAt("key", 24, base)
			}
		}()
	}
	close(start)
	wg.Wait()
	savers.Wait()
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.summaryAt(24, base)["key"]
	want := int64(writers * perWriter)
	if got.Requests != want || got.TokenRequests != want || got.TotalTokens != want {
		t.Fatalf("concurrent persisted stats = %+v, want %d requests", got, want)
	}
	if snap := reopened.snapshotAt("key", 24, base); len(snap.Recent) != recentPerKey {
		t.Fatalf("recent count = %d, want %d", len(snap.Recent), recentPerKey)
	}
}

func TestStoredEventAllowlistJSON(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	raw, err := json.Marshal(storedEventFrom(reqlog.Event{
		Time:         now,
		RequestID:    "id",
		KeyID:        "key",
		Path:         "/v1/chat/completions",
		Account:      "nickname",
		Model:        "m",
		Status:       200,
		OK:           true,
		Outcome:      reqlog.OutcomeSuccess,
		DurationMs:   1,
		TTFBMs:       1,
		Attempts:     1,
		PromptTokens: 1,
		TotalTokens:  1,
		TokenKnown:   true,
		Credit:       1,
		HasCredit:    true,
		ClientIP:     "127.0.0.1",
		UserAgent:    "ua",
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"account", "nickname", "key_id", "client_ip", "user_agent", "ttfb_ms"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("stored event contains %q: %s", forbidden, raw)
		}
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
