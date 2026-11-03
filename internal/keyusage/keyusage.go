// Package keyusage persists per-distributed-API-key request aggregates.
//
// The recorder is deliberately independent from reqlog's optional JSONL archive.
// It keeps hourly buckets for 90 days, folds older hours into permanent UTC day
// buckets, and persists only a small allowlist of request monitoring fields.
package keyusage

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/reqlog"
)

const (
	fileVersion      = 1
	hourlyRetention  = 90 * 24 * time.Hour
	saveInterval     = 30 * time.Second
	recentPerKey     = 20
	maxModelRunes    = 128
	unknownModelName = "(unknown)"

	hourGranularity = "hour"
	dayGranularity  = "day"
)

// Stats is one aggregate over requests, tokens, credit, and latency.
// LastUsedAt is omitted when no request has ever been observed.
type Stats struct {
	Requests         int64      `json:"requests"`
	Errors           int64      `json:"errors"`
	SuccessRate      float64    `json:"success_rate"`
	PromptTokens     int64      `json:"prompt_tokens"`
	CompletionTokens int64      `json:"completion_tokens"`
	TotalTokens      int64      `json:"total_tokens"`
	TokenRequests    int64      `json:"token_requests"`
	Credit           float64    `json:"credit"`
	CreditRequests   int64      `json:"credit_requests"`
	AvgLatencyMs     float64    `json:"avg_latency_ms"`
	LastUsedAt       *time.Time `json:"last_used_at,omitempty"`
}

// ModelStats is Stats for one model.
type ModelStats struct {
	Model string `json:"model"`
	Stats
}

// SeriesPoint is Stats for one UTC hour or UTC day.
type SeriesPoint struct {
	Time time.Time `json:"time"`
	Stats
}

// Snapshot is the complete usage view for one key and time window.
type Snapshot struct {
	Totals      Stats          `json:"totals"`
	ByModel     []ModelStats   `json:"by_model"`
	Series      []SeriesPoint  `json:"series"`
	Recent      []reqlog.Event `json:"recent"`
	Hours       int            `json:"hours"`
	From        *time.Time     `json:"from"`
	To          *time.Time     `json:"to"`
	Granularity string         `json:"granularity"`
}

type bucketID struct {
	keyID       string
	model       string
	granularity string
	start       time.Time
}

type counters struct {
	requests         int64
	errors           int64
	promptTokens     int64
	completionTokens int64
	totalTokens      int64
	tokenRequests    int64
	credit           float64
	creditRequests   int64
	latencySumMs     int64
	latencySamples   int64
	lastUsedAt       time.Time
}

func (c *counters) addEvent(e reqlog.Event) {
	c.requests++
	if !e.OK {
		c.errors++
	}
	if e.TokenKnown {
		c.promptTokens += nonNegative(e.PromptTokens)
		c.completionTokens += nonNegative(e.CompletionTokens)
		c.totalTokens += nonNegative(e.TotalTokens)
		c.tokenRequests++
	}
	if e.HasCredit && finiteNonNegative(e.Credit) {
		c.credit += e.Credit
		c.creditRequests++
	}
	c.latencySumMs += nonNegative(e.DurationMs)
	c.latencySamples++
	if e.Time.After(c.lastUsedAt) {
		c.lastUsedAt = e.Time
	}
}

func (c *counters) merge(other counters) {
	c.requests += other.requests
	c.errors += other.errors
	c.promptTokens += other.promptTokens
	c.completionTokens += other.completionTokens
	c.totalTokens += other.totalTokens
	c.tokenRequests += other.tokenRequests
	c.credit += other.credit
	c.creditRequests += other.creditRequests
	c.latencySumMs += other.latencySumMs
	c.latencySamples += other.latencySamples
	if other.lastUsedAt.After(c.lastUsedAt) {
		c.lastUsedAt = other.lastUsedAt
	}
}

func (c counters) stats(lastUsedAt time.Time) Stats {
	s := Stats{
		Requests:         c.requests,
		Errors:           c.errors,
		PromptTokens:     c.promptTokens,
		CompletionTokens: c.completionTokens,
		TotalTokens:      c.totalTokens,
		TokenRequests:    c.tokenRequests,
		Credit:           c.credit,
		CreditRequests:   c.creditRequests,
	}
	if c.requests > 0 {
		s.SuccessRate = float64(c.requests-c.errors) / float64(c.requests) * 100
		if s.SuccessRate < 0 {
			s.SuccessRate = 0
		} else if s.SuccessRate > 100 {
			s.SuccessRate = 100
		}
	}
	if c.latencySamples > 0 {
		s.AvgLatencyMs = float64(c.latencySumMs) / float64(c.latencySamples)
	}
	s.LastUsedAt = timePointer(lastUsedAt)
	return s
}

type keyState struct {
	lastUsedAt time.Time
	recent     []storedEvent
}

type runState struct {
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// Recorder is a concurrency-safe key usage recorder.
type Recorder struct {
	mu       sync.Mutex
	path     string
	buckets  map[bucketID]counters
	keys     map[string]*keyState
	dirty    bool
	revision uint64

	saveMu sync.Mutex
	runMu  sync.Mutex
	run    *runState
}

// Record records one final gateway request. Requests with an empty key ID are
// ignored. Callers should invoke it exactly once at the gateway's final exit.
func (r *Recorder) Record(e reqlog.Event) {
	r.recordAt(e, time.Now().UTC())
}

func (r *Recorder) recordAt(e reqlog.Event, now time.Time) {
	if r == nil || e.KeyID == "" {
		return
	}
	now = now.UTC()
	if e.Time.IsZero() {
		e.Time = now
	} else {
		e.Time = e.Time.UTC()
	}
	e.Model = normalizeModel(e.Model)
	e.PromptTokens = nonNegative(e.PromptTokens)
	e.CompletionTokens = nonNegative(e.CompletionTokens)
	e.TotalTokens = nonNegative(e.TotalTokens)
	e.DurationMs = nonNegative(e.DurationMs)
	if !finiteNonNegative(e.Credit) {
		e.Credit = 0
		e.HasCredit = false
	}

	start := e.Time.Truncate(time.Hour)
	granularity := hourGranularity
	cutoff := now.Truncate(time.Hour).Add(-hourlyRetention)
	if start.Before(cutoff) {
		start = utcDayStart(e.Time)
		granularity = dayGranularity
	}
	id := bucketID{
		keyID:       e.KeyID,
		model:       e.Model,
		granularity: granularity,
		start:       start,
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.buckets[id]
	c.addEvent(e)
	r.buckets[id] = c

	state := r.keys[e.KeyID]
	if state == nil {
		state = &keyState{}
		r.keys[e.KeyID] = state
	}
	if e.Time.After(state.lastUsedAt) {
		state.lastUsedAt = e.Time
	}
	state.recent = insertRecent(state.recent, storedEventFrom(e))
	r.markDirtyLocked()
}

// Summary returns per-key stats for the selected window. hours=0 means all
// history; other supported values are 24, 168, and 720. LastUsedAt is kept
// independently from buckets and remains the key's most recent observed time.
func (r *Recorder) Summary(hours int) map[string]Stats {
	return r.summaryAt(hours, time.Now().UTC())
}

func (r *Recorder) summaryAt(hours int, now time.Time) map[string]Stats {
	if r == nil {
		return map[string]Stats{}
	}
	now = now.UTC()
	w := makeWindow(hours, now)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.rollupLocked(now)

	aggregates := make(map[string]counters)
	for id, c := range r.buckets {
		if !w.containsBucket(id.start, now) {
			continue
		}
		a := aggregates[id.keyID]
		a.merge(c)
		aggregates[id.keyID] = a
	}

	out := make(map[string]Stats, len(r.keys))
	for id, state := range r.keys {
		out[id] = aggregates[id].stats(state.lastUsedAt)
	}
	return out
}

// Snapshot returns one key's totals, model breakdown, time series, and recent
// requests. Unknown or deleted keys return an empty snapshot without mutating
// retained history.
func (r *Recorder) Snapshot(id string, hours int) Snapshot {
	return r.snapshotAt(id, hours, time.Now().UTC())
}

func (r *Recorder) snapshotAt(id string, hours int, now time.Time) Snapshot {
	now = now.UTC()
	w := makeWindow(hours, now)
	snap := Snapshot{
		ByModel:     make([]ModelStats, 0),
		Series:      make([]SeriesPoint, 0),
		Recent:      make([]reqlog.Event, 0),
		Hours:       w.hours,
		From:        timePointer(w.from),
		To:          timePointer(w.to),
		Granularity: w.granularity,
	}
	if r == nil {
		return snap
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.rollupLocked(now)

	state := r.keys[id]
	var total counters
	byModel := make(map[string]counters)
	series := make(map[time.Time]counters)
	for bid, c := range r.buckets {
		if bid.keyID != id || !w.containsBucket(bid.start, now) {
			continue
		}
		total.merge(c)

		m := byModel[bid.model]
		m.merge(c)
		byModel[bid.model] = m

		point := bid.start
		if w.granularity == dayGranularity {
			point = utcDayStart(bid.start)
		}
		p := series[point]
		p.merge(c)
		series[point] = p
	}

	var lastUsed time.Time
	if state != nil {
		lastUsed = state.lastUsedAt
	}
	snap.Totals = total.stats(lastUsed)

	modelNames := make([]string, 0, len(byModel))
	for model := range byModel {
		modelNames = append(modelNames, model)
	}
	sort.Strings(modelNames)
	for _, model := range modelNames {
		snap.ByModel = append(snap.ByModel, ModelStats{
			Model: model,
			Stats: byModel[model].stats(byModel[model].lastUsedAt),
		})
	}

	points := make([]time.Time, 0, len(series))
	for point := range series {
		points = append(points, point)
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Before(points[j]) })
	for _, point := range points {
		c := series[point]
		snap.Series = append(snap.Series, SeriesPoint{
			Time:  point,
			Stats: c.stats(c.lastUsedAt),
		})
	}

	if state != nil {
		for _, stored := range state.recent {
			if !w.containsEvent(stored.Time, now) {
				continue
			}
			event := eventFromStored(stored)
			event.KeyID = id
			snap.Recent = append(snap.Recent, event)
			if len(snap.Recent) == recentPerKey {
				break
			}
		}
	}
	return snap
}

type window struct {
	hours       int
	from        time.Time
	to          time.Time
	bounded     bool
	granularity string
}

func makeWindow(hours int, now time.Time) window {
	now = now.UTC()
	if hours <= 0 {
		return window{hours: 0, granularity: dayGranularity}
	}
	switch hours {
	case 24, 168, 720:
	default:
		hours = 24
	}
	return window{
		hours:       hours,
		from:        now.Truncate(time.Hour).Add(-time.Duration(hours-1) * time.Hour),
		to:          now,
		bounded:     true,
		granularity: hourGranularity,
	}
}

func (w window) containsBucket(start, now time.Time) bool {
	if start.After(now) {
		return false
	}
	return !w.bounded || !start.Before(w.from)
}

func (w window) containsEvent(at, now time.Time) bool {
	if at.After(now) {
		return false
	}
	return !w.bounded || !at.Before(w.from)
}

func (r *Recorder) rollupAt(now time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rollupLocked(now.UTC())
}

func (r *Recorder) rollupLocked(now time.Time) {
	cutoff := now.UTC().Truncate(time.Hour).Add(-hourlyRetention)
	changed := false
	for id, c := range r.buckets {
		if id.granularity != hourGranularity || !id.start.Before(cutoff) {
			continue
		}
		dstID := bucketID{
			keyID:       id.keyID,
			model:       id.model,
			granularity: dayGranularity,
			start:       utcDayStart(id.start),
		}
		dst := r.buckets[dstID]
		dst.merge(c)
		r.buckets[dstID] = dst
		delete(r.buckets, id)
		changed = true
	}
	if changed {
		r.markDirtyLocked()
	}
}

func (r *Recorder) markDirtyLocked() {
	r.dirty = true
	r.revision++
}

func insertRecent(recent []storedEvent, event storedEvent) []storedEvent {
	pos := sort.Search(len(recent), func(i int) bool {
		return !recent[i].Time.After(event.Time)
	})
	recent = append(recent, storedEvent{})
	copy(recent[pos+1:], recent[pos:])
	recent[pos] = event
	if len(recent) > recentPerKey {
		recent = recent[:recentPerKey]
	}
	return recent
}

func normalizeModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return unknownModelName
	}
	runes := []rune(model)
	// Keep hostile model names bounded. This is a display/storage limit; names
	// longer than 128 Unicode code points are truncated and may share a bucket.
	if len(runes) > maxModelRunes {
		model = string(runes[:maxModelRunes])
	}
	return model
}

func utcDayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func timePointer(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

func nonNegative(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func finiteNonNegative(v float64) bool {
	return v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}
