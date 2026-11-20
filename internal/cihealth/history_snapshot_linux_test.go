package cihealth

import (
	"bytes"
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
	if err != nil || len(rows) != 2 || rows[0].Key != retry.Key || rows[0].Clock.Conclusion != ConclusionSuccess ||
		!rows[1].AsOf.Equal(later) || rows[1].Key != after.Key || rows[1].Jobs[0].Clock.Conclusion != ConclusionFailure ||
		len(rows[1].Jobs[0].Steps) != 1 || rows[1].Jobs[0].Steps[0].Key.Number != after.Jobs[0].Steps[0].Key.Number {
		t.Fatalf("known result moved scope or missing child was filled: %+v / %v", rows, err)
	}
}
