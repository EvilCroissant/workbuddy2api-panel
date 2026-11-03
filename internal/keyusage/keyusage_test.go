package keyusage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/reqlog"
)

func TestStatsWindowsAndDeletedKeyHistory(t *testing.T) {
	r, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 3, 12, 34, 56, 0, time.UTC)

	r.recordAt(reqlog.Event{
		Time:             now.Add(-30 * time.Minute),
		RequestID:        "ok",
		KeyID:            "key-a",
		Path:             "/v1/chat/completions",
		Model:            "alpha",
		Status:           200,
		OK:               true,
		Outcome:          reqlog.OutcomeSuccess,
		DurationMs:       100,
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
		TokenKnown:       true,
		Credit:           1.25,
		HasCredit:        true,
	}, now)
	r.recordAt(reqlog.Event{
		Time:         now.Add(-70 * time.Minute),
		RequestID:    "bad",
		KeyID:        "key-a",
		Path:         "/v1/chat/completions",
		Model:        "alpha",
		Status:       502,
		OK:           false,
		Outcome:      reqlog.OutcomeStreamError,
		DurationMs:   300,
		PromptTokens: 999,
		TotalTokens:  999,
		TokenKnown:   false,
		Credit:       99,
		HasCredit:    false,
	}, now)
	r.recordAt(reqlog.Event{
		Time:        now.Add(-25 * time.Hour),
		RequestID:   "outside",
		KeyID:       "key-a",
		Path:        "/v1/chat/completions",
		Model:       "beta",
		Status:      200,
		OK:          true,
		Outcome:     reqlog.OutcomeSuccess,
		DurationMs:  50,
		TotalTokens: 7,
		TokenKnown:  true,
		Credit:      0.5,
		HasCredit:   true,
	}, now)
	r.recordAt(reqlog.Event{
		Time:       now.Add(-100 * time.Hour),
		RequestID:  "deleted-only",
		KeyID:      "deleted",
		Path:       "/v1/search/messages",
		Model:      "search",
		Status:     200,
		OK:         true,
		Outcome:    reqlog.OutcomeSuccess,
		DurationMs: 75,
		TokenKnown: false,
	}, now)

	summary := r.summaryAt(24, now)
	a := summary["key-a"]
	if a.Requests != 2 || a.Errors != 1 || a.SuccessRate != 50 {
		t.Fatalf("key-a request stats = %+v", a)
	}
	if a.PromptTokens != 10 || a.CompletionTokens != 5 || a.TotalTokens != 15 || a.TokenRequests != 1 {
		t.Fatalf("key-a token stats = %+v", a)
	}
	if a.Credit != 1.25 || a.CreditRequests != 1 || a.AvgLatencyMs != 200 {
		t.Fatalf("key-a credit/latency stats = %+v", a)
	}
	if a.LastUsedAt == nil || !a.LastUsedAt.Equal(now.Add(-30*time.Minute)) {
		t.Fatalf("key-a last used = %v", a.LastUsedAt)
	}
	if got := summary["deleted"]; got.Requests != 0 || got.LastUsedAt == nil ||
		!got.LastUsedAt.Equal(now.Add(-100*time.Hour)) {
		t.Fatalf("deleted history = %+v, want zero window stats with retained last_used_at", got)
	}

	snap := r.snapshotAt("key-a", 24, now)
	wantFrom := now.Truncate(time.Hour).Add(-23 * time.Hour)
	if snap.Hours != 24 || snap.Granularity != hourGranularity {
		t.Fatalf("window metadata = hours:%d granularity:%q", snap.Hours, snap.Granularity)
	}
	if snap.From == nil || !snap.From.Equal(wantFrom) {
		t.Fatalf("from = %v, want %s", snap.From, wantFrom)
	}
	if snap.To == nil || !snap.To.Equal(now) {
		t.Fatalf("to = %v, want %s", snap.To, now)
	}
	if snap.Totals.Requests != 2 || snap.Totals.TotalTokens != 15 {
		t.Fatalf("snapshot totals = %+v", snap.Totals)
	}
	if len(snap.ByModel) != 1 || snap.ByModel[0].Model != "alpha" ||
		snap.ByModel[0].Requests != 2 {
		t.Fatalf("by_model = %+v", snap.ByModel)
	}
	if len(snap.Series) != 2 {
		t.Fatalf("series = %+v, want two hour points", snap.Series)
	}
	if len(snap.Recent) != 2 || snap.Recent[0].RequestID != "ok" ||
		snap.Recent[1].RequestID != "bad" {
		t.Fatalf("recent = %+v", snap.Recent)
	}
	for _, event := range snap.Recent {
		if event.KeyID != "key-a" {
			t.Fatalf("recent key_id = %q, want key-a: %+v", event.KeyID, event)
		}
	}

	week := r.snapshotAt("key-a", 168, now)
	if week.Totals.Requests != 3 || week.Totals.TotalTokens != 22 ||
		week.Totals.CreditRequests != 2 {
		t.Fatalf("week totals = %+v", week.Totals)
	}
	if len(week.ByModel) != 2 || week.ByModel[0].Model != "alpha" ||
		week.ByModel[1].Model != "beta" {
		t.Fatalf("week models = %+v", week.ByModel)
	}

	all := r.snapshotAt("key-a", 0, now)
	if all.From != nil || all.To != nil || all.Granularity != dayGranularity {
		t.Fatalf("all-history metadata = from:%v to:%v granularity:%q",
			all.From, all.To, all.Granularity)
	}
	if all.Totals.Requests != 3 || len(all.Series) != 2 {
		t.Fatalf("all-history snapshot = totals:%+v series:%+v", all.Totals, all.Series)
	}

	invalid := r.snapshotAt("key-a", 48, now)
	if invalid.Hours != 24 {
		t.Fatalf("unsupported hours normalized to %d, want 24", invalid.Hours)
	}
}

func TestNilRecorderMethodsAndEmptyKey(t *testing.T) {
	var nilRecorder *Recorder
	if got := nilRecorder.Summary(24); got == nil || len(got) != 0 {
		t.Fatalf("nil Summary = %#v", got)
	}
	if snap := nilRecorder.Snapshot("missing", 24); snap.Totals.Requests != 0 ||
		snap.Hours != 24 || snap.Recent == nil || snap.ByModel == nil || snap.Series == nil {
		t.Fatalf("nil Snapshot = %+v", snap)
	}
	nilRecorder.Record(reqlog.Event{KeyID: "ignored"})
	nilRecorder.Start()
	nilRecorder.Stop()
	if err := nilRecorder.Save(); err != nil {
		t.Fatal(err)
	}

	r, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	r.Record(reqlog.Event{RequestID: "no-key", Model: "m"})
	if got := r.Summary(0); len(got) != 0 {
		t.Fatalf("empty key was recorded: %+v", got)
	}
}

func TestModelNameIsBounded(t *testing.T) {
	r, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	long := strings.Repeat("模", maxModelRunes+40)
	r.recordAt(reqlog.Event{
		Time:    now,
		KeyID:   "key",
		Model:   long,
		OK:      true,
		Status:  200,
		Outcome: reqlog.OutcomeSuccess,
	}, now)
	snap := r.snapshotAt("key", 24, now)
	if len(snap.ByModel) != 1 {
		t.Fatalf("by_model = %+v", snap.ByModel)
	}
	if got := []rune(snap.ByModel[0].Model); len(got) != maxModelRunes {
		t.Fatalf("model rune length = %d, want %d", len(got), maxModelRunes)
	}
}

func TestStatsJSONShape(t *testing.T) {
	empty, err := json.Marshal(Stats{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), "last_used_at") {
		t.Fatalf("empty Stats JSON includes last_used_at: %s", empty)
	}

	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	raw, err := json.Marshal(Snapshot{
		Totals:      Stats{Requests: 1, LastUsedAt: &now},
		ByModel:     []ModelStats{{Model: "m", Stats: Stats{Requests: 1}}},
		Series:      []SeriesPoint{{Time: now, Stats: Stats{Requests: 1}}},
		Recent:      []reqlog.Event{},
		Hours:       24,
		From:        &now,
		To:          &now,
		Granularity: hourGranularity,
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["totals"].(map[string]any)["success_rate"]; !ok {
		t.Fatalf("snapshot JSON missing totals.success_rate: %s", raw)
	}
	byModel := decoded["by_model"].([]any)
	if _, ok := byModel[0].(map[string]any)["model"]; !ok {
		t.Fatalf("by_model does not flatten model/stats: %s", raw)
	}
	series := decoded["series"].([]any)
	if _, ok := series[0].(map[string]any)["time"]; !ok {
		t.Fatalf("series does not include time: %s", raw)
	}
}
