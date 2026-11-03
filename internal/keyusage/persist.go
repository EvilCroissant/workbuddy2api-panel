package keyusage

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/reqlog"
)

type diskFile struct {
	Version int          `json:"version"`
	SavedAt time.Time    `json:"saved_at"`
	Keys    []diskKey    `json:"keys"`
	Buckets []diskBucket `json:"buckets"`
}

type diskKey struct {
	ID         string        `json:"id"`
	LastUsedAt *time.Time    `json:"last_used_at,omitempty"`
	Recent     []storedEvent `json:"recent,omitempty"`
}

type diskBucket struct {
	KeyID            string    `json:"key_id"`
	Model            string    `json:"model"`
	Granularity      string    `json:"granularity"`
	Start            time.Time `json:"start"`
	Requests         int64     `json:"requests"`
	Errors           int64     `json:"errors"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	TotalTokens      int64     `json:"total_tokens"`
	TokenRequests    int64     `json:"token_requests"`
	Credit           float64   `json:"credit"`
	CreditRequests   int64     `json:"credit_requests"`
	LatencySumMs     int64     `json:"latency_sum_ms"`
	LatencySamples   int64     `json:"latency_samples"`
	LastUsedAt       time.Time `json:"last_used_at"`
}

// storedEvent is an explicit privacy allowlist. Do not embed reqlog.Event here:
// future fields would then be persisted automatically.
type storedEvent struct {
	Time             time.Time `json:"time"`
	RequestID        string    `json:"request_id"`
	Path             string    `json:"path"`
	Model            string    `json:"model,omitempty"`
	Status           int       `json:"status"`
	OK               bool      `json:"ok"`
	Outcome          string    `json:"outcome"`
	DurationMs       int64     `json:"duration_ms"`
	Attempts         int       `json:"attempts,omitempty"`
	PromptTokens     int64     `json:"prompt_tokens,omitempty"`
	CompletionTokens int64     `json:"completion_tokens,omitempty"`
	TotalTokens      int64     `json:"total_tokens,omitempty"`
	TokenKnown       bool      `json:"token_known"`
	Credit           float64   `json:"credit,omitempty"`
	HasCredit        bool      `json:"credit_known"`
}

func storedEventFrom(e reqlog.Event) storedEvent {
	s := storedEvent{
		Time:       e.Time.UTC(),
		RequestID:  e.RequestID,
		Path:       e.Path,
		Model:      normalizeModel(e.Model),
		Status:     e.Status,
		OK:         e.OK,
		Outcome:    e.Outcome,
		DurationMs: nonNegative(e.DurationMs),
		Attempts:   e.Attempts,
	}
	if s.Attempts < 0 {
		s.Attempts = 0
	}
	if e.TokenKnown {
		s.PromptTokens = nonNegative(e.PromptTokens)
		s.CompletionTokens = nonNegative(e.CompletionTokens)
		s.TotalTokens = nonNegative(e.TotalTokens)
		s.TokenKnown = true
	}
	if e.HasCredit && finiteNonNegative(e.Credit) {
		s.Credit = e.Credit
		s.HasCredit = true
	}
	return s
}

func eventFromStored(s storedEvent) reqlog.Event {
	e := reqlog.Event{
		Time:       s.Time.UTC(),
		RequestID:  s.RequestID,
		Path:       s.Path,
		Model:      normalizeModel(s.Model),
		Status:     s.Status,
		OK:         s.OK,
		Outcome:    s.Outcome,
		DurationMs: nonNegative(s.DurationMs),
		Attempts:   s.Attempts,
		TokenKnown: s.TokenKnown,
		HasCredit:  s.HasCredit,
	}
	if e.Attempts < 0 {
		e.Attempts = 0
	}
	if s.TokenKnown {
		e.PromptTokens = nonNegative(s.PromptTokens)
		e.CompletionTokens = nonNegative(s.CompletionTokens)
		e.TotalTokens = nonNegative(s.TotalTokens)
	}
	if s.HasCredit && finiteNonNegative(s.Credit) {
		e.Credit = s.Credit
	}
	return e
}

// Open loads path or creates an empty in-memory recorder when the file is
// missing. A malformed file is returned as an error and is never overwritten.
func Open(path string) (*Recorder, error) {
	r := &Recorder{
		path:    path,
		buckets: make(map[bucketID]counters),
		keys:    make(map[string]*keyState),
	}
	if path == "" {
		return r, nil
	}

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// The first Save creates a valid empty file.
		r.dirty = true
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read key usage: %w", err)
	}

	var file diskFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("decode key usage: %w", err)
	}
	if file.Version != fileVersion {
		return nil, fmt.Errorf("unsupported key usage version %d", file.Version)
	}
	if err := r.loadDisk(file); err != nil {
		return nil, fmt.Errorf("validate key usage: %w", err)
	}
	if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("secure key usage file: %w", err)
		}
	}
	r.rollupLocked(time.Now().UTC())
	return r, nil
}

func (r *Recorder) loadDisk(file diskFile) error {
	for _, key := range file.Keys {
		if key.ID == "" {
			return errors.New("empty key ID")
		}
		if _, exists := r.keys[key.ID]; exists {
			return fmt.Errorf("duplicate key ID %q", key.ID)
		}
		state := &keyState{}
		if key.LastUsedAt != nil {
			state.lastUsedAt = key.LastUsedAt.UTC()
		}
		for _, recent := range key.Recent {
			if recent.Time.IsZero() {
				return fmt.Errorf("key %q has recent event without time", key.ID)
			}
			recent.Time = recent.Time.UTC()
			recent.Model = normalizeModel(recent.Model)
			state.recent = insertRecent(state.recent, recent)
			if recent.Time.After(state.lastUsedAt) {
				state.lastUsedAt = recent.Time
			}
		}
		r.keys[key.ID] = state
	}

	seen := make(map[bucketID]struct{}, len(file.Buckets))
	for _, disk := range file.Buckets {
		if _, ok := r.keys[disk.KeyID]; !ok {
			return fmt.Errorf("bucket references unknown key %q", disk.KeyID)
		}
		if disk.Granularity != hourGranularity && disk.Granularity != dayGranularity {
			return fmt.Errorf("invalid bucket granularity %q", disk.Granularity)
		}
		start := disk.Start.UTC()
		if start.IsZero() || !alignedBucket(start, disk.Granularity) {
			return fmt.Errorf("invalid bucket start %s", disk.Start)
		}
		id := bucketID{
			keyID:       disk.KeyID,
			model:       normalizeModel(disk.Model),
			granularity: disk.Granularity,
			start:       start,
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate bucket for key %q model %q", id.keyID, id.model)
		}
		seen[id] = struct{}{}

		c := counters{
			requests:         disk.Requests,
			errors:           disk.Errors,
			promptTokens:     disk.PromptTokens,
			completionTokens: disk.CompletionTokens,
			totalTokens:      disk.TotalTokens,
			tokenRequests:    disk.TokenRequests,
			credit:           disk.Credit,
			creditRequests:   disk.CreditRequests,
			latencySumMs:     disk.LatencySumMs,
			latencySamples:   disk.LatencySamples,
			lastUsedAt:       disk.LastUsedAt.UTC(),
		}
		if err := validateCounters(c); err != nil {
			return fmt.Errorf("key %q model %q: %w", id.keyID, id.model, err)
		}
		r.buckets[id] = c
		state := r.keys[disk.KeyID]
		if c.lastUsedAt.After(state.lastUsedAt) {
			state.lastUsedAt = c.lastUsedAt
		}
	}
	return nil
}

func validateCounters(c counters) error {
	switch {
	case c.requests < 0:
		return errors.New("negative requests")
	case c.errors < 0 || c.errors > c.requests:
		return errors.New("invalid errors")
	case c.promptTokens < 0 || c.completionTokens < 0 || c.totalTokens < 0:
		return errors.New("negative tokens")
	case c.tokenRequests < 0 || c.tokenRequests > c.requests:
		return errors.New("invalid token_requests")
	case !finiteNonNegative(c.credit):
		return errors.New("invalid credit")
	case c.creditRequests < 0 || c.creditRequests > c.requests:
		return errors.New("invalid credit_requests")
	case c.latencySumMs < 0:
		return errors.New("negative latency sum")
	case c.latencySamples < 0 || c.latencySamples > c.requests:
		return errors.New("invalid latency samples")
	case c.requests > 0 && c.lastUsedAt.IsZero():
		return errors.New("missing bucket last_used_at")
	}
	return nil
}

func alignedBucket(t time.Time, granularity string) bool {
	switch granularity {
	case hourGranularity:
		return t.Equal(t.Truncate(time.Hour))
	case dayGranularity:
		return t.Equal(utcDayStart(t))
	default:
		return false
	}
}

// Save atomically persists the current state. Saves are serialized, and a
// failed write leaves the recorder dirty so a later Save can retry.
func (r *Recorder) Save() error {
	if r == nil || r.path == "" {
		return nil
	}
	r.saveMu.Lock()
	defer r.saveMu.Unlock()

	now := time.Now().UTC()
	r.mu.Lock()
	r.rollupLocked(now)
	if !r.dirty {
		r.mu.Unlock()
		return nil
	}
	file := r.diskFileLocked(now)
	revision := r.revision
	r.mu.Unlock()

	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal key usage: %w", err)
	}
	if err := atomicWrite(r.path, raw); err != nil {
		return err
	}

	r.mu.Lock()
	if r.revision == revision {
		r.dirty = false
	}
	r.mu.Unlock()
	return nil
}

func (r *Recorder) diskFileLocked(now time.Time) diskFile {
	file := diskFile{
		Version: fileVersion,
		SavedAt: now,
		Keys:    make([]diskKey, 0, len(r.keys)),
		Buckets: make([]diskBucket, 0, len(r.buckets)),
	}
	keyIDs := make([]string, 0, len(r.keys))
	for id := range r.keys {
		keyIDs = append(keyIDs, id)
	}
	sort.Strings(keyIDs)
	for _, id := range keyIDs {
		state := r.keys[id]
		recent := append([]storedEvent(nil), state.recent...)
		file.Keys = append(file.Keys, diskKey{
			ID:         id,
			LastUsedAt: timePointer(state.lastUsedAt),
			Recent:     recent,
		})
	}

	ids := make([]bucketID, 0, len(r.buckets))
	for id := range r.buckets {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if ids[i].keyID != ids[j].keyID {
			return ids[i].keyID < ids[j].keyID
		}
		if ids[i].model != ids[j].model {
			return ids[i].model < ids[j].model
		}
		if ids[i].granularity != ids[j].granularity {
			return ids[i].granularity < ids[j].granularity
		}
		return ids[i].start.Before(ids[j].start)
	})
	for _, id := range ids {
		c := r.buckets[id]
		file.Buckets = append(file.Buckets, diskBucket{
			KeyID:            id.keyID,
			Model:            id.model,
			Granularity:      id.granularity,
			Start:            id.start,
			Requests:         c.requests,
			Errors:           c.errors,
			PromptTokens:     c.promptTokens,
			CompletionTokens: c.completionTokens,
			TotalTokens:      c.totalTokens,
			TokenRequests:    c.tokenRequests,
			Credit:           c.credit,
			CreditRequests:   c.creditRequests,
			LatencySumMs:     c.latencySumMs,
			LatencySamples:   c.latencySamples,
			LastUsedAt:       c.lastUsedAt,
		})
	}
	return file
}

func atomicWrite(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create key usage directory: %w", err)
	}
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary key usage file: %w", err)
	}
	tmp := file.Name()
	defer os.Remove(tmp)

	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return fmt.Errorf("chmod temporary key usage file: %w", err)
	}
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return fmt.Errorf("write temporary key usage file: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync temporary key usage file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary key usage file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace key usage file: %w", err)
	}
	return nil
}

// Start launches a 30-second save loop. Repeated calls are idempotent.
func (r *Recorder) Start() {
	if r == nil {
		return
	}
	r.runMu.Lock()
	if r.run != nil {
		r.runMu.Unlock()
		return
	}
	run := &runState{
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	r.run = run
	r.runMu.Unlock()

	go func() {
		defer close(run.done)
		ticker := time.NewTicker(saveInterval)
		defer ticker.Stop()
		r.saveLoop(run.stop, ticker.C)
	}()
}

func (r *Recorder) saveLoop(stop <-chan struct{}, ticks <-chan time.Time) {
	for {
		select {
		case <-stop:
			return
		case <-ticks:
			if err := r.Save(); err != nil {
				log.Printf("[keyusage] periodic save failed: %v", err)
			}
		}
	}
}

// Stop stops the background loop and flushes the current state. It is safe to
// call repeatedly and safe to call without Start.
func (r *Recorder) Stop() {
	if r == nil {
		return
	}
	r.runMu.Lock()
	run := r.run
	if run != nil {
		run.stopOnce.Do(func() { close(run.stop) })
	}
	r.runMu.Unlock()

	if run != nil {
		<-run.done
		r.runMu.Lock()
		if r.run == run {
			r.run = nil
		}
		r.runMu.Unlock()
	}
	if err := r.Save(); err != nil {
		log.Printf("[keyusage] flush on stop failed: %v", err)
	}
}
