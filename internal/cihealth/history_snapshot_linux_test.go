package cihealth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func snapshotRoot(t *testing.T) (string, *os.Root) {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return path, root
}

func snapshotOpen(t *testing.T, root *os.Root, initialize bool) *History {
	t.Helper()
	h, err := OpenHistory(t.Context(), HistoryInput{Enabled: true, Root: root, Initialize: initialize})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func snapAttempt(s HistoryScope, run, attempt int64, now time.Time) AttemptFacts {
	k := AttemptKey{s.Source, s.RepositoryID, s.WorkflowID, run, attempt}
	j := JobKey{k, 7}
	c := Clock{Phase: Completed, StartedAt: now.Add(-time.Minute), CompletedAt: now, ObservedAt: now}
	return AttemptFacts{Key: k, AsOf: now, Clock: c, Jobs: []JobFacts{{Key: j, Clock: c, Steps: []StepFacts{{Key: StepKey{j, 4}, Clock: c}}}}}
}

func sizedSnapAttempt(s HistoryScope, run int64, observed time.Time, jobs, steps int) AttemptFacts {
	a := snapAttempt(s, run, 1, observed)
	a.Jobs = nil
	for i := 0; i < jobs; i++ {
		j := JobFacts{Key: JobKey{a.Key, int64(i + 1)}, Clock: a.Clock}
		stepsInJob := steps / jobs
		if i < steps%jobs {
			stepsInJob++
		}
		for n := 0; n < stepsInJob; n++ {
			j.Steps = append(j.Steps, StepFacts{Key: StepKey{j.Key, int64(n + 1)}, Clock: a.Clock})
		}
		a.Jobs = append(a.Jobs, j)
	}
	return a
}

func TestSnapshotHistoryRecentPrefixWholeAttemptRecordBudget(t *testing.T) {
	path, root := snapshotRoot(t)
	h := snapshotOpen(t, root, true)
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	s := HistoryScope{"fixture", 901, 100}
	seed := make([]AttemptFacts, 38)
	for i := range seed {
		seed[i] = sizedSnapAttempt(s, int64(i+3), now, 28, 279)
	}
	write := func(expected, next int64, page []AttemptFacts) {
		t.Helper()
		if err := h.Apply(t.Context(), s, HistoryCursor{Revision: expected}, HistoryCursor{Revision: next}, page, now); err != nil {
			t.Fatal(err)
		}
	}
	write(0, 1, seed) // 11,704 whole records.
	p, err := h.RecentPrefix(t.Context(), s, 38, now)
	if err != nil || p.Limited != "" || len(p.Attempts) != 38 {
		t.Fatalf("exactly the attempt cap is not truncation: %d %q %v", len(p.Attempts), p.Limited, err)
	}
	write(1, 2, []AttemptFacts{
		sizedSnapAttempt(s, 2, now, 28, 279), // 39 x 308 = 12,012, not a storage fault.
		snapAttempt(s, 1, 1, now),            // Would fit individually; must not skip newer run 2.
	})
	before, err := os.ReadFile(filepath.Join(path, historyFile))
	if err != nil {
		t.Fatal(err)
	}
	p, err = h.RecentPrefix(t.Context(), s, 64, now)
	if err != nil || p.Limited != HistorySelectionRecordLimit || len(p.Attempts) != 38 ||
		p.Attempts[0].Key.RunID != 40 || p.Attempts[37].Key.RunID != 3 {
		t.Fatalf("expected newest contiguous 38 complete attempts: %d %q %v", len(p.Attempts), p.Limited, err)
	}
	for _, a := range p.Attempts {
		if len(a.Jobs) != 28 || historyRecordCount(a) != 308 {
			t.Fatal("selected partial attempt", a.Key)
		}
	}
	p.Attempts[37].Jobs[0].Steps[0].Key.Number = 999
	p, err = h.RecentPrefix(t.Context(), s, 64, now)
	if err != nil || len(p.Attempts) != 38 || p.Attempts[37].Jobs[0].Steps[0].Key.Number != 1 {
		t.Fatal("returned shared mutable data", err)
	}
	if rows, err := h.Recent(t.Context(), s, 64, now); rows != nil || !errors.Is(err, ErrHistory) {
		t.Fatal("legacy strict read contract changed", err)
	}
	if c, err := h.Cursor(t.Context(), s); c.Revision != 2 || err != nil {
		t.Fatal("selection or strict read changed underlying cursor", c, err)
	}
	if after, err := os.ReadFile(filepath.Join(path, historyFile)); err != nil || !bytes.Equal(before, after) {
		t.Fatal("bounded selection changed retained snapshot bytes", err)
	}

	// An exactly full record page has no marker unless another eligible whole
	// attempt exists. A different scope cannot establish truncation.
	exact := s
	exact.WorkflowID++
	last := sizedSnapAttempt(exact, 1, now, 28, 267) // 296 remaining records.
	for i := range seed {
		seed[i] = sizedSnapAttempt(exact, int64(i+2), now, 28, 279)
	}
	if err := h.Apply(t.Context(), exact, HistoryCursor{}, HistoryCursor{Revision: 1}, append(seed, last), now); err != nil {
		t.Fatal(err)
	}
	p, err = h.RecentPrefix(t.Context(), exact, 64, now)
	if err != nil || p.Limited != "" || len(p.Attempts) != 39 || p.Attempts[38].Key != last.Key {
		t.Fatalf("exact record fit falsely described as limited: %d %q %v", len(p.Attempts), p.Limited, err)
	}
}

func TestSnapshotHistoryRecentPrefixAttemptCapAndActualFaults(t *testing.T) {
	path, root := snapshotRoot(t)
	h := snapshotOpen(t, root, true)
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	s := HistoryScope{"fixture", 902, 100}
	rows := make([]AttemptFacts, 64)
	for i := range rows {
		rows[i] = snapAttempt(s, int64(i+1), 1, now)
	}
	current, next := HistoryCursor{Revision: 1}, HistoryCursor{Revision: 2}
	if err := h.Apply(t.Context(), s, HistoryCursor{}, current, rows, now); err != nil {
		t.Fatal(err)
	}
	// Positive rows from another scope or beyond now do not set a marker.
	other := s
	other.WorkflowID++
	if err := h.Apply(t.Context(), other, HistoryCursor{}, current, []AttemptFacts{snapAttempt(other, 100, 1, now)}, now); err != nil {
		t.Fatal(err)
	}
	future := now.Add(time.Second)
	if err := h.Apply(t.Context(), s, current, next, []AttemptFacts{snapAttempt(s, 65, 1, future)}, future); err != nil {
		t.Fatal(err)
	}
	exact, err := h.RecentPrefix(t.Context(), s, 64, now)
	if err != nil || exact.Limited != "" || len(exact.Attempts) != 64 || exact.Attempts[63].Key.RunID != 1 {
		t.Fatalf("without another eligible row exact cap should be unmarked: %d %q %v", len(exact.Attempts), exact.Limited, err)
	}
	p, err := h.RecentPrefix(t.Context(), s, 64, future)
	if err != nil || p.Limited != HistorySelectionAttemptLimit || len(p.Attempts) != 64 ||
		p.Attempts[0].Key.RunID != 65 || p.Attempts[63].Key.RunID != 2 {
		t.Fatalf("eligible additional row was not reported: %d %q %v", len(p.Attempts), p.Limited, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if canceled, err := h.RecentPrefix(ctx, s, 64, future); canceled.Attempts != nil || canceled.Limited != "" || !errors.Is(err, ErrHistory) {
		t.Fatal("cancellation turned into a partial success", err)
	}
	if err := os.WriteFile(filepath.Join(path, historyStage), []byte("unexpected stage"), 0600); err != nil {
		t.Fatal(err)
	}
	if invalid, err := h.RecentPrefix(t.Context(), s, 64, future); invalid.Attempts != nil || invalid.Limited != "" || !errors.Is(err, ErrHistory) {
		t.Fatal("actual held store fault turned into a partial success", err)
	}
}

func TestSnapshotHistoryExactAtomicPageReviewAndRestart(t *testing.T) {
	_, root := snapshotRoot(t)
	h := snapshotOpen(t, root, true)
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	s := HistoryScope{"fixture", 9007199254740993, 44}
	a, b := snapAttempt(s, 20, 1, now), snapAttempt(s, 20, 2, now)
	n := HistoryCursor{1, 20}
	if err := h.Apply(t.Context(), s, HistoryCursor{}, n, []AttemptFacts{a, b}, now); err != nil {
		t.Fatal(err)
	}
	if err := h.Apply(t.Context(), s, HistoryCursor{}, n, []AttemptFacts{b, a}, now); err != nil {
		t.Fatal("exact replay conflicted", err)
	}
	contradiction := snapAttempt(s, 20, 1, now)
	contradiction.Jobs = nil
	first := snapAttempt(s, 10, 1, now)
	if err := h.Apply(t.Context(), s, n, HistoryCursor{2, 21}, []AttemptFacts{first, contradiction}, now); !errors.Is(err, ErrHistoryConflict) {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	h = snapshotOpen(t, root, false)
	if c, err := h.Cursor(t.Context(), s); c != n || err != nil {
		t.Fatal("checkpoint not atomic", c, err)
	}
	rows, err := h.Recent(t.Context(), s, 128, now)
	if err != nil || len(rows) != 2 || rows[0].Key != b.Key || rows[1].Key != a.Key || rows[0].Jobs[0].Steps[0].Key.Number != 4 {
		t.Fatal("exact keys not restored", err, len(rows))
	}
	rows[0].Jobs[0].Steps[0].Key.Number = 90
	rows, err = h.Recent(t.Context(), s, 1, now)
	if err != nil || len(rows) != 1 || rows[0].Jobs[0].Steps[0].Key.Number != 4 {
		t.Fatal("returned mutable history", err)
	}
	other := s
	other.WorkflowID++
	if rows, err = h.Recent(t.Context(), other, 1, now); err != nil || len(rows) != 0 {
		t.Fatal("cross-workflow data", rows, err)
	}
	expiry := now.Add(historyRetention)
	if rows, err := h.Recent(t.Context(), s, 1, expiry); err != nil || len(rows) != 0 {
		t.Fatal("expired data visible", rows, err)
	}
	if err := h.Apply(t.Context(), s, n, HistoryCursor{2, 0}, nil, expiry); err != nil {
		t.Fatal(err)
	}
	if len(h.state.Attempts) != 0 {
		t.Fatal("did not prune whole attempts")
	}
}

func TestSnapshotHistoryHeldDirectoryAndSingleWriter(t *testing.T) {
	path, root := snapshotRoot(t)
	h := snapshotOpen(t, root, true)
	if next, err := OpenHistory(t.Context(), HistoryInput{Enabled: true, Root: root}); next != nil || !errors.Is(err, ErrHistory) {
		t.Fatal("second directory owner", err)
	}
	// Replacing the startup pathname cannot redirect operations performed
	// through the held root, and the caller need not retain its original FD.
	moved := path + "-moved"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(moved) })
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	s := HistoryScope{"fixture", 12, 44}
	if err := h.Apply(t.Context(), s, HistoryCursor{}, HistoryCursor{1, 20}, []AttemptFacts{snapAttempt(s, 20, 1, now)}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, historyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrote to replacement directory", err)
	}
	pinned, err := os.OpenRoot(moved)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	if err = h.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := snapshotOpen(t, pinned, false)
	if c, err := reopened.Cursor(t.Context(), s); c.Revision != 1 || err != nil {
		t.Fatal("held target lost update", c, err)
	}
}

func TestSnapshotHistoryRefusesForeignFixedFilesAndDirtyStage(t *testing.T) {
	path, root := snapshotRoot(t)
	if next, err := OpenHistory(t.Context(), HistoryInput{Root: root, Initialize: true}); next != nil || err != nil {
		t.Fatal("not default-off")
	}
	fixed := filepath.Join(path, historyFile)
	foreign := filepath.Join(path, "foreign")
	if err := os.WriteFile(foreign, []byte("do not change"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, link := range []func(string, string) error{os.Symlink, os.Link} {
		if err := link(foreign, fixed); err != nil {
			t.Fatal(err)
		}
		for _, init := range []bool{true, false} {
			if h, err := OpenHistory(t.Context(), HistoryInput{Enabled: true, Root: root, Initialize: init}); h != nil || !errors.Is(err, ErrHistory) {
				t.Fatal("adopted foreign link", err)
			}
		}
		if err := os.Remove(fixed); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mkfifo(fixed, 0600); err != nil {
		t.Fatal(err)
	}
	if next, err := OpenHistory(t.Context(), HistoryInput{Enabled: true, Root: root}); next != nil || !errors.Is(err, ErrHistory) {
		t.Fatal("adopted FIFO", err)
	}
	if err := os.Remove(fixed); err != nil {
		t.Fatal(err)
	}
	h := snapshotOpen(t, root, true)
	stage := filepath.Join(path, historyStage)
	if err := os.WriteFile(stage, []byte("unresolved stage"), 0600); err != nil {
		t.Fatal(err)
	}
	s := HistoryScope{"fixture", 2, 4}
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	if err := h.Apply(t.Context(), s, HistoryCursor{}, HistoryCursor{1, 1}, nil, now); !errors.Is(err, ErrHistory) {
		t.Fatal("ignored stage", err)
	}
	held := stage + ".reviewing"
	if err := os.Rename(stage, held); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Cursor(t.Context(), s); !errors.Is(err, ErrHistory) {
		t.Fatal("dirty stage fence was cleared without an independently reopened store", err)
	}
	if err := os.Rename(held, stage); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if next, err := OpenHistory(t.Context(), HistoryInput{Enabled: true, Root: root}); next != nil || !errors.Is(err, ErrHistory) {
		t.Fatal("automatically adopted dirty startup", err)
	}
	if data, err := os.ReadFile(stage); err != nil || string(data) != "unresolved stage" {
		t.Fatal("mutated stage", err)
	}
	if data, err := os.ReadFile(foreign); err != nil || string(data) != "do not change" {
		t.Fatal("mutated unrelated file", err)
	}
}

func TestSnapshotHistoryFencesCurrentReplacementAndMalformedPayload(t *testing.T) {
	path, root := snapshotRoot(t)
	h := snapshotOpen(t, root, true)
	f := filepath.Join(path, historyFile)
	payload, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, payload, 0600); err != nil {
		t.Fatal(err)
	}
	s := HistoryScope{"fixture", 2, 4}
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	if err := h.Apply(t.Context(), s, HistoryCursor{}, HistoryCursor{1, 1}, nil, now); !errors.Is(err, ErrHistory) {
		t.Fatal("a same-byte replacement became writable", err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{
		[]byte(`{"Format":"garm-ci-health-history-v1","Checkpoints":[],"Attempts":[],"Unknown":1}`),
		[]byte(`{"Format":"discarded","Format":"garm-ci-health-history-v1","Checkpoints":[],"Attempts":[]}`),
		make([]byte, historyMaxBytes+1),
	} {
		if err := os.WriteFile(f, bad, 0600); err != nil {
			t.Fatal(err)
		}
		if next, err := OpenHistory(t.Context(), HistoryInput{Enabled: true, Root: root}); next != nil || !errors.Is(err, ErrHistory) {
			t.Fatal("adopted malformed state", err)
		}
	}
}

func TestSnapshotHistorySerializesCAS(t *testing.T) {
	_, root := snapshotRoot(t)
	h := snapshotOpen(t, root, true)
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	s := HistoryScope{"fixture", 2, 4}
	var wg sync.WaitGroup
	result := make(chan error, 2)
	for _, k := range []int64{1, 2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result <- h.Apply(t.Context(), s, HistoryCursor{}, HistoryCursor{1, k}, []AttemptFacts{snapAttempt(s, k, 1, now)}, now)
		}()
	}
	wg.Wait()
	a, b := <-result, <-result
	if (a == nil) == (b == nil) || a != nil && !errors.Is(a, ErrHistoryConflict) || b != nil && !errors.Is(b, ErrHistoryConflict) {
		t.Fatal("did not serialize exact attempts", a, b)
	}
}

// This is the observable on-disk interruption state after rename but before
// durability acknowledgement: a canonical new current file, no staging name,
// and the separately persisted intent. Reopening must not adopt it.
func TestSnapshotHistoryUnresolvedIntentBlocksCanonicalReopen(t *testing.T) {
	path, root := snapshotRoot(t)
	h := snapshotOpen(t, root, true)
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	s := HistoryScope{"fixture", 2, 4}
	if err := h.Apply(t.Context(), s, HistoryCursor{}, HistoryCursor{1, 1}, []AttemptFacts{snapAttempt(s, 1, 1, now)}, now); err != nil {
		t.Fatal(err)
	}
	if !h.noPending() {
		t.Fatal("fully acknowledged publication left an intent")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(path, historyFile)
	before, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(path, historyIntent)
	if err := os.WriteFile(pending, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, initialize := range []bool{false, true} {
		if got, err := OpenHistory(t.Context(), HistoryInput{Enabled: true, Root: root, Initialize: initialize}); got != nil || !errors.Is(err, ErrHistoryUncertain) {
			t.Fatal("canonical data was adopted despite unresolved publication", err)
		}
	}
	if after, err := os.ReadFile(current); err != nil || string(before) != string(after) {
		t.Fatal("recovery refusal mutated data", err)
	}
	if _, err := os.Stat(pending); err != nil {
		t.Fatal("startup removed intent", err)
	}
}

func TestSnapshotHistoryOptionalConclusionsAreCanonical(t *testing.T) {
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	s := HistoryScope{"fixture", 9007199254740993, 44}
	a := snapAttempt(s, 20, 1, now)
	state := historyState{
		Format:      historyFormat,
		Checkpoints: []historyCheckpoint{{Scope: s, Cursor: HistoryCursor{1, 20}, Digest: [32]byte{1}}},
		Attempts:    []AttemptFacts{a},
	}
	legacy, err := json.Marshal(state)
	if err != nil || bytes.Contains(legacy, []byte(`"Conclusion"`)) {
		t.Fatalf("missing data changed existing encoding: %v", err)
	}
	read, err := decodeHistory(legacy)
	if err != nil || read.Attempts[0].Clock.Conclusion != ConclusionUnknown {
		t.Fatalf("legacy record refused or filled: %v", err)
	}
	state.Attempts[0].Clock.Conclusion = ConclusionCancelled
	state.Attempts[0].Jobs[0].Clock.Conclusion = ConclusionTimedOut
	state.Attempts[0].Jobs[0].Steps[0].Clock.Conclusion = ConclusionFailure
	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	read, err = decodeHistory(payload)
	if err != nil || read.Attempts[0].Clock.Conclusion != ConclusionCancelled ||
		read.Attempts[0].Jobs[0].Clock.Conclusion != ConclusionTimedOut ||
		read.Attempts[0].Jobs[0].Steps[0].Clock.Conclusion != ConclusionFailure {
		t.Fatalf("explicit results did not round-trip: %v", err)
	}
	for _, changed := range [][]byte{
		bytes.Replace(payload, []byte(`"Conclusion":"cancelled"`), []byte(`"Conclusion":"dynamic-data"`), 1),
		bytes.Replace(payload, []byte(`"Conclusion":"cancelled"`), []byte(`"Conclusion":null`), 1),
		bytes.Replace(payload, []byte(`"Conclusion":"cancelled"`), []byte(`"Conclusion":"cancelled","Conclusion":"cancelled"`), 1),
	} {
		if _, err := decodeHistory(changed); !errors.Is(err, ErrHistory) {
			t.Fatalf("unsafe conclusion accepted in history: %v", err)
		}
	}
}

func TestSnapshotHistoryRejectsAPageThatWouldLoseKnownConclusions(t *testing.T) {
	for label, change := range map[string]func(*AttemptFacts){
		"attempt contradiction": func(a *AttemptFacts) { a.Clock.Conclusion = ConclusionCancelled },
		"attempt omission":      func(a *AttemptFacts) { a.Clock.Conclusion = ConclusionUnknown },
		"job contradiction":     func(a *AttemptFacts) { a.Jobs[0].Clock.Conclusion = ConclusionTimedOut },
		"job omission":          func(a *AttemptFacts) { a.Jobs[0].Clock.Conclusion = ConclusionUnknown },
		"job disappeared":       func(a *AttemptFacts) { a.Jobs = nil },
		"job replaced by another ID": func(a *AttemptFacts) {
			a.Jobs[0].Key.JobID++
			a.Jobs[0].Steps[0].Key.Job = a.Jobs[0].Key
		},
		"step contradiction":              func(a *AttemptFacts) { a.Jobs[0].Steps[0].Clock.Conclusion = ConclusionSuccess },
		"step omission":                   func(a *AttemptFacts) { a.Jobs[0].Steps[0].Clock.Conclusion = ConclusionUnknown },
		"step disappeared":                func(a *AttemptFacts) { a.Jobs[0].Steps = nil },
		"step replaced by another number": func(a *AttemptFacts) { a.Jobs[0].Steps[0].Key.Number++ },
		"step observation missing":        func(a *AttemptFacts) { a.Jobs[0].Steps[0].Clock.ObservedAt = time.Time{} },
		"step observation older": func(a *AttemptFacts) {
			c := &a.Jobs[0].Steps[0].Clock
			c.ObservedAt = c.ObservedAt.Add(-2 * time.Minute)
			c.StartedAt, c.CompletedAt = time.Time{}, time.Time{}
		},
	} {
		t.Run(label, func(t *testing.T) {
			path, root := snapshotRoot(t)
			h := snapshotOpen(t, root, true)
			now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
			s := HistoryScope{"fixture", 9007199254740993, 44}
			known := func(at time.Time) AttemptFacts {
				a := snapAttempt(s, 20, 1, at)
				a.Clock.Conclusion = ConclusionFailure
				a.Jobs[0].Clock.Conclusion = ConclusionFailure
				a.Jobs[0].Steps[0].Clock.Conclusion = ConclusionFailure
				return a
			}
			prior, next := HistoryCursor{1, 20}, HistoryCursor{2, 21}
			if err := h.Apply(t.Context(), s, HistoryCursor{}, prior, []AttemptFacts{known(now)}, now); err != nil {
				t.Fatal(err)
			}
			sealed, err := os.ReadFile(filepath.Join(path, historyFile))
			if err != nil {
				t.Fatal(err)
			}
			later := now.Add(time.Minute)
			incoming := known(later)
			change(&incoming)
			if _, err = Measure(incoming); err != nil {
				t.Fatalf("fixture failed basic validation, not replacement guard: %v", err)
			}
			retry := snapAttempt(s, 20, 2, later)
			if err = h.Apply(t.Context(), s, prior, next, []AttemptFacts{retry, incoming}, later); !errors.Is(err, ErrHistoryConflict) {
				t.Fatalf("uncertain page not refused: %v", err)
			}
			preserved, err := os.ReadFile(filepath.Join(path, historyFile))
			if err != nil || !bytes.Equal(sealed, preserved) || !h.noPending() {
				t.Fatal("rejected page touched durable state", err)
			}
			if err = h.Close(); err != nil {
				t.Fatal(err)
			}
			h = snapshotOpen(t, root, false)
			cursor, err := h.Cursor(t.Context(), s)
			rows, readErr := h.Recent(t.Context(), s, 2, later)
			if err != nil || readErr != nil || cursor != prior || len(rows) != 1 || !rows[0].AsOf.Equal(now) ||
				rows[0].Clock.Conclusion != ConclusionFailure || rows[0].Jobs[0].Clock.Conclusion != ConclusionFailure ||
				rows[0].Jobs[0].Steps[0].Clock.Conclusion != ConclusionFailure {
				t.Fatalf("rejected page refreshed or replaced historical facts: %v %v", err, readErr)
			}
		})
	}
}

func TestSnapshotHistoryCompatibleResultsDoNotFillMissingIdentity(t *testing.T) {
	_, root := snapshotRoot(t)
	h := snapshotOpen(t, root, true)
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	s := HistoryScope{"fixture", 9007199254740993, 44}
	before := snapAttempt(s, 20, 1, now)
	before.Clock.Conclusion = ConclusionFailure
	before.Jobs[0].Steps[0].Clock.Conclusion = ConclusionFailure
	unknownStep := before.Jobs[0].Steps[0]
	unknownStep.Key.Number++
	unknownStep.Clock.Conclusion = ConclusionUnknown
	before.Jobs[0].Steps = append(before.Jobs[0].Steps, unknownStep)
	prior, next := HistoryCursor{1, 20}, HistoryCursor{2, 21}
	if err := h.Apply(t.Context(), s, HistoryCursor{}, prior, []AttemptFacts{before}, now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Minute)
	missingOnlyParent := snapAttempt(s, 20, 1, later)
	missingOnlyParent.Clock.Conclusion = ConclusionFailure
	missingOnlyParent.Jobs = nil // Its job had no result, but did contain a known step.
	if err := h.Apply(t.Context(), s, prior, next, []AttemptFacts{missingOnlyParent}, later); !errors.Is(err, ErrHistoryConflict) {
		t.Fatal("a missing job hid a known step result", err)
	}
	after := snapAttempt(s, 20, 1, later)
	after.Clock.Conclusion = ConclusionFailure
	after.Jobs[0].Clock.Conclusion = ConclusionFailure // Positive evidence may fill previous unknown.
	after.Jobs[0].Steps[0].Clock.Conclusion = ConclusionFailure
	retry := snapAttempt(s, 20, 2, later)
	retry.Clock.Conclusion = ConclusionSuccess // A rerun is not the original identity.
	if err := h.Apply(t.Context(), s, prior, next, []AttemptFacts{after, retry}, later); err != nil {
		t.Fatal(err)
	}
	rows, err := h.Recent(t.Context(), s, 2, later)
	if err != nil || len(rows) != 3 || rows[0].Key != retry.Key || rows[0].Clock.Conclusion != ConclusionSuccess ||
		!rows[1].AsOf.Equal(later) || rows[1].Key != after.Key || rows[1].Jobs[0].Clock.Conclusion != ConclusionFailure ||
		len(rows[1].Jobs[0].Steps) != 1 || rows[1].Jobs[0].Steps[0].Key.Number != after.Jobs[0].Steps[0].Key.Number ||
		!rows[2].AsOf.Equal(now) || rows[2].Key != after.Key || len(rows[2].Jobs[0].Steps) != 2 {
		t.Fatalf("known result moved scope, filled a missing child, or dropped the prior version: %+v / %v", rows, err)
	}
}

func TestHistoryIgnoresLiveJobCreationClock(t *testing.T) {
	now := time.Now().UTC()
	jobKey := JobKey{Attempt: AttemptKey{Source: "fixture", RepositoryID: 1, WorkflowID: 2, RunID: 3, Attempt: 1}, JobID: 4}
	a := AttemptFacts{Key: jobKey.Attempt, AsOf: now, Clock: Clock{Phase: Completed}, Jobs: []JobFacts{{
		Key: jobKey, Clock: Clock{Phase: Completed, StartedAt: now.Add(-time.Minute), CompletedAt: now, ObservedAt: now},
	}}}
	legacy, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	a.Jobs[0].CreatedAt = now.Add(-time.Hour)
	changed, err := json.Marshal(a)
	if err != nil || !bytes.Equal(legacy, changed) {
		t.Fatalf("changed retained encoding: %v", err)
	}
	out, err := historyCopyAttempt(a)
	if err != nil || !out.Jobs[0].CreatedAt.IsZero() {
		t.Fatalf("retained live-only time: %v", err)
	}
	if a.Jobs[0].CreatedAt.IsZero() {
		t.Fatal("history normalization mutated the live snapshot")
	}
}

// A later positive observation of the same exact attempt that omits its
// endpoints must not destroy the previously measured result: both stay
// retained as their own versions, the latest one visibly incomplete.
func TestSnapshotHistorySparseLaterObservationKeepsPriorMeasuredVersion(t *testing.T) {
	path, root := snapshotRoot(t)
	h := snapshotOpen(t, root, true)
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	s := HistoryScope{"fixture", 9007199254740993, 44}
	measured := snapAttempt(s, 20, 1, now)
	measured.Clock.Conclusion = ConclusionSuccess
	measured.Jobs[0].Clock.Conclusion = ConclusionSuccess
	measured.Jobs[0].Steps[0].Clock.Conclusion = ConclusionSuccess
	prior, next := HistoryCursor{1, 20}, HistoryCursor{2, 21}
	if err := h.Apply(t.Context(), s, HistoryCursor{}, prior, []AttemptFacts{measured}, now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(2 * time.Minute)
	sparse := measured
	sparse.AsOf = later
	sparse.Clock.StartedAt, sparse.Clock.CompletedAt = time.Time{}, time.Time{}
	sparse.Jobs[0].Clock.StartedAt, sparse.Jobs[0].Clock.CompletedAt = time.Time{}, time.Time{}
	sparse.Jobs[0].Steps[0].Clock.StartedAt, sparse.Jobs[0].Steps[0].Clock.CompletedAt = time.Time{}, time.Time{}
	sparse.Jobs[0].CreatedAt = later // Live-only creation must not reach retained bytes.
	if err := h.Apply(t.Context(), s, prior, next, []AttemptFacts{sparse}, later); err != nil {
		t.Fatal(err)
	}
	sealed, err := os.ReadFile(filepath.Join(path, historyFile))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := h.Recent(t.Context(), s, 1, later)
	if err != nil || len(rows) != 2 {
		t.Fatalf("expected exactly the sparse and measured versions: %+v, %v", rows, err)
	}
	if !rows[0].AsOf.Equal(later) || rows[0].Clock.Conclusion != ConclusionSuccess ||
		!rows[0].Clock.StartedAt.IsZero() || !rows[0].Clock.CompletedAt.IsZero() ||
		len(rows[0].Jobs) != 1 || !rows[0].Jobs[0].Clock.StartedAt.IsZero() {
		t.Fatalf("latest row is not the visibly incomplete observation: %+v", rows[0])
	}
	if !rows[1].AsOf.Equal(now) || rows[1].Clock.StartedAt.IsZero() || rows[1].Clock.CompletedAt.IsZero() ||
		len(rows[1].Jobs[0].Steps) != 1 || rows[1].Jobs[0].Steps[0].Clock.StartedAt.IsZero() {
		t.Fatalf("previously measured result was not preserved as its own version: %+v", rows[1])
	}
	if timing, err := Measure(rows[1]); err != nil || timing.Wall != (Span{Basis: ObservedEndpoints, Duration: time.Minute}) {
		t.Fatalf("prior measurement no longer measures: %+v, %v", timing, err)
	}
	// An exact replay of the accepted transition is a durable no-op.
	if err := h.Apply(t.Context(), s, prior, next, []AttemptFacts{sparse}, later); err != nil {
		t.Fatalf("exact replay conflicted: %v", err)
	}
	if after, err := os.ReadFile(filepath.Join(path, historyFile)); err != nil || !bytes.Equal(sealed, after) {
		t.Fatal("replay mutated durable state", err)
	}
	// Restart around the committed versions preserves both and the cursor.
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	h = snapshotOpen(t, root, false)
	if c, err := h.Cursor(t.Context(), s); err != nil || c != next {
		t.Fatal("cursor advanced ahead of facts or was lost", c, err)
	}
	rows, err = h.Recent(t.Context(), s, 1, later)
	if err != nil || len(rows) != 2 || !rows[0].AsOf.Equal(later) || !rows[1].AsOf.Equal(now) {
		t.Fatalf("reopened store lost a version: %+v, %v", rows, err)
	}
	// The sparse row of one attempt never blocks unrelated facts in the same scope.
	unrelated := snapAttempt(s, 21, 1, later)
	if err := h.Apply(t.Context(), s, next, HistoryCursor{3, 22}, []AttemptFacts{unrelated}, later); err != nil {
		t.Fatal(err)
	}
	if rows, err := h.Recent(t.Context(), s, 2, later); err != nil || len(rows) != 3 || rows[0].Key.RunID != 21 {
		t.Fatalf("unrelated observation was blocked by the sparse record: %+v, %v", rows, err)
	}
}

func TestSnapshotHistoryVersionOrderingReplayAndPageRules(t *testing.T) {
	path, root := snapshotRoot(t)
	h := snapshotOpen(t, root, true)
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	s := HistoryScope{"fixture", 9007199254740993, 44}
	first := snapAttempt(s, 20, 1, now)
	first.Jobs[0].Steps[0].Clock.Conclusion = ConclusionSuccess
	prior, next := HistoryCursor{1, 20}, HistoryCursor{2, 21}
	if err := h.Apply(t.Context(), s, HistoryCursor{}, prior, []AttemptFacts{first}, now); err != nil {
		t.Fatal(err)
	}
	sealed, err := os.ReadFile(filepath.Join(path, historyFile))
	if err != nil {
		t.Fatal(err)
	}
	older := snapAttempt(s, 20, 1, now.Add(-time.Second))
	if err := h.Apply(t.Context(), s, prior, next, []AttemptFacts{older}, now); !errors.Is(err, ErrHistoryConflict) {
		t.Fatal("out-of-order version accepted", err)
	}
	reworded := snapAttempt(s, 20, 1, now) // Same observation time, different facts.
	reworded.Jobs = nil
	if err := h.Apply(t.Context(), s, prior, HistoryCursor{2, 19}, []AttemptFacts{reworded}, now); !errors.Is(err, ErrHistoryConflict) {
		t.Fatal("same-time contradiction accepted", err)
	}
	if err := h.Apply(t.Context(), s, HistoryCursor{}, prior, []AttemptFacts{first}, now); err != nil {
		t.Fatal("exact replay conflicted", err)
	}
	if after, err := os.ReadFile(filepath.Join(path, historyFile)); err != nil || !bytes.Equal(sealed, after) {
		t.Fatal("replay mutated durable state", err)
	}
	if c, err := h.Cursor(t.Context(), s); err != nil || c != prior {
		t.Fatal("replay moved the cursor", c, err)
	}
	if err := h.Apply(t.Context(), s, HistoryCursor{}, HistoryCursor{1, 21}, []AttemptFacts{snapAttempt(s, 21, 1, now)}, now); !errors.Is(err, ErrHistoryConflict) {
		t.Fatal("stale expected cursor accepted", err)
	}
	if err := h.Apply(t.Context(), s, prior, next, []AttemptFacts{first, older}, now); !errors.Is(err, ErrHistory) {
		t.Fatal("page with two versions of one attempt accepted", err)
	}
	// A newer version that regresses a known step conclusion is refused.
	later := now.Add(time.Minute)
	regressed := snapAttempt(s, 20, 1, later)
	regressed.Jobs[0].Steps[0].Clock.Conclusion = ConclusionUnknown
	if _, err := Measure(regressed); err != nil {
		t.Fatalf("fixture failed basic validation: %v", err)
	}
	if err := h.Apply(t.Context(), s, prior, next, []AttemptFacts{regressed}, later); !errors.Is(err, ErrHistoryConflict) {
		t.Fatal("terminal regression accepted", err)
	}
	if after, err := os.ReadFile(filepath.Join(path, historyFile)); err != nil || !bytes.Equal(sealed, after) {
		t.Fatal("rejected version mutated durable state", err)
	}
}

func TestSnapshotHistoryPurgesExpiredVersionsByObservationTime(t *testing.T) {
	_, root := snapshotRoot(t)
	h := snapshotOpen(t, root, true)
	first := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	s := HistoryScope{"fixture", 9007199254740993, 44}
	if err := h.Apply(t.Context(), s, HistoryCursor{}, HistoryCursor{1, 1}, []AttemptFacts{snapAttempt(s, 20, 1, first)}, first); err != nil {
		t.Fatal(err)
	}
	if rows, err := h.Recent(t.Context(), s, 2, first); err != nil || len(rows) != 1 || rows[0].Key.RunID != 20 {
		t.Fatalf("fresh version not visible at its own observation time: %+v, %v", rows, err)
	}
	later := first.Add(historyRetention).Add(time.Hour)
	current := snapAttempt(s, 21, 1, later)
	if err := h.Apply(t.Context(), s, HistoryCursor{1, 1}, HistoryCursor{2, 2}, []AttemptFacts{current}, later); err != nil {
		t.Fatal(err)
	}
	if rows, err := h.Recent(t.Context(), s, 2, later); err != nil || len(rows) != 1 || rows[0].Key.RunID != 21 {
		t.Fatalf("expired version was not purged by its own observation time: %+v, %v", rows, err)
	}
	if rows, err := h.Recent(t.Context(), s, 2, first); err != nil || len(rows) != 0 {
		t.Fatalf("reading at an older wall resurrected purged data: %+v, %v", rows, err)
	}
}

// Versions of different attempts interleave in the stored newest-first order.
// At the attempt limit the scan must still return every retained version of
// each selected attempt, never a partial group cut off at the limit.
func TestSnapshotHistoryRecentPrefixInterleavedVersionsAtAttemptLimit(t *testing.T) {
	_, root := snapshotRoot(t)
	h := snapshotOpen(t, root, true)
	base := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	now := base.Add(5 * time.Minute)
	s := HistoryScope{"fixture", 9007199254740993, 44}
	aNew := snapAttempt(s, 30, 1, base.Add(4*time.Minute))
	b := snapAttempt(s, 31, 1, base.Add(3*time.Minute))
	c := snapAttempt(s, 32, 1, base.Add(2*time.Minute))
	aOld := snapAttempt(s, 30, 1, base.Add(time.Minute))
	if err := h.Apply(t.Context(), s, HistoryCursor{}, HistoryCursor{1, 1}, []AttemptFacts{aOld, c}, now); err != nil {
		t.Fatal(err)
	}
	if err := h.Apply(t.Context(), s, HistoryCursor{1, 1}, HistoryCursor{2, 2}, []AttemptFacts{aNew, b}, now); err != nil {
		t.Fatal(err)
	}
	p, err := h.RecentPrefix(t.Context(), s, 2, now)
	if err != nil || p.Limited != HistorySelectionAttemptLimit || len(p.Attempts) != 3 {
		t.Fatalf("expected two complete groups with the third reported limited: %+v, %v", p, err)
	}
	versions := map[int64]int{}
	for _, row := range p.Attempts {
		versions[row.Key.RunID]++
		if row.Key.RunID == 32 {
			t.Fatal("unselected attempt leaked into the selection")
		}
	}
	if versions[30] != 2 || versions[31] != 1 {
		t.Fatalf("interleaved older version of a selected attempt was dropped: %+v", p.Attempts)
	}
	rows, err := h.Recent(t.Context(), s, 2, now)
	if err != nil || len(rows) != 3 || rows[2].Key.RunID != 30 || !rows[2].AsOf.Equal(base.Add(time.Minute)) {
		t.Fatalf("strict bounded read did not return the complete groups: %+v, %v", rows, err)
	}
}

// At the record limit a group that does not fit stops the selection, but
// older versions of already selected attempts that sort after it must still
// be included: the limit never splits a selected group.
func TestSnapshotHistoryRecentPrefixInterleavedVersionsAtRecordLimit(t *testing.T) {
	_, root := snapshotRoot(t)
	h := snapshotOpen(t, root, true)
	base := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	now := base.Add(5 * time.Minute)
	s := HistoryScope{"fixture", 9007199254740993, 44}
	small := func(run int64, at time.Time) AttemptFacts {
		return sizedSnapAttempt(s, run, at, 100, 396) // 497 records.
	}
	big := sizedSnapAttempt(s, 99, base.Add(3*time.Minute), 1000, 9900) // 10,901 records.
	aNew := small(30, base.Add(4*time.Minute))
	c := small(32, base.Add(2*time.Minute))
	aOld := small(30, base.Add(time.Minute))
	if records := historyRecordCount(big); 2*historyRecordCount(aNew)+records > historyMaxPageRecords ||
		2*historyRecordCount(aNew)+records+historyRecordCount(c) <= historyMaxPageRecords {
		t.Fatalf("fixture no longer brackets the record budget: %d", records)
	}
	if err := h.Apply(t.Context(), s, HistoryCursor{}, HistoryCursor{1, 1}, []AttemptFacts{aOld, c}, now); err != nil {
		t.Fatal(err)
	}
	if err := h.Apply(t.Context(), s, HistoryCursor{1, 1}, HistoryCursor{2, 2}, []AttemptFacts{aNew, big}, now); err != nil {
		t.Fatal(err)
	}
	p, err := h.RecentPrefix(t.Context(), s, 64, now)
	if err != nil || p.Limited != HistorySelectionRecordLimit || len(p.Attempts) != 3 {
		t.Fatalf("expected two complete groups with the third reported limited: %+v, %v", p, err)
	}
	versions := map[int64]int{}
	for _, row := range p.Attempts {
		versions[row.Key.RunID]++
		if row.Key.RunID == 32 {
			t.Fatal("unselected attempt leaked into the selection")
		}
	}
	if versions[30] != 2 || versions[99] != 1 {
		t.Fatalf("interleaved older version of a selected attempt was dropped: %+v", p.Attempts)
	}
	before, err := os.ReadFile(filepath.Join(root.Name(), historyFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.RecentPrefix(t.Context(), s, 64, now); err != nil {
		t.Fatal(err)
	}
	if after, err := os.ReadFile(filepath.Join(root.Name(), historyFile)); err != nil || !bytes.Equal(before, after) {
		t.Fatal("bounded selection changed retained snapshot bytes", err)
	}
}
