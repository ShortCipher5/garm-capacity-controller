package cihealth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"sort"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	historyFile           = "ci-health-history-v1.json"
	historyStage          = "ci-health-history-v1.stage"
	historyIntent         = "ci-health-history-v1.intent"
	historyFormat         = "garm-ci-health-history-v1"
	historyRetention      = 30 * 24 * time.Hour
	historyMaxRecords     = 50_000
	historyMaxScopes      = 64
	historyMaxPage        = 64
	historyMaxRecent      = 128
	historyMaxPageRecords = 12_000
	historyMaxBytes       = 16 << 20
)

var (
	ErrHistory          = errors.New("CI history unavailable")
	ErrHistoryConflict  = errors.New("CI history checkpoint or observation changed")
	ErrHistoryUncertain = errors.New("CI history publication uncertain; review dedicated storage")
)

type HistoryScope struct {
	Source                   string
	RepositoryID, WorkflowID int64
}

func (s HistoryScope) valid() bool {
	return validKey(AttemptKey{s.Source, s.RepositoryID, s.WorkflowID, 1, 1})
}

// HistoryCursor is a local CAS checkpoint, never GitHub coverage or authorization.
type HistoryCursor struct{ Revision, Checkpoint int64 }

type HistoryInput struct {
	Enabled bool
	// Root must already be under this deployment's uninterrupted exclusive
	// service-user custody. The store duplicates its directory descriptor; the
	// caller can close its Root afterwards. No directory path is accepted.
	Root *os.Root
	// Initialize publishes the empty store only if the exact slot is absent.
	// Otherwise an existing valid snapshot is required.
	Initialize bool
}

type historyCheckpoint struct {
	Scope  HistoryScope
	Cursor HistoryCursor
	Digest [sha256.Size]byte
}
type historyState struct {
	Format      string
	Checkpoints []historyCheckpoint
	Attempts    []AttemptFacts
}

type History struct {
	mu        sync.Mutex
	root      *os.Root
	dir       *os.File
	currentFD *os.File
	current   os.FileInfo
	state     historyState
	failed    bool
}

// OpenHistory is opt-in and not connected to startup. Flock excludes a second
// cooperating opener of the same Linux directory; it is not protection from
// privileged or same-UID external changes. Startup must independently provide
// dedicated storage, with rename and file/directory fsync on one filesystem.
func OpenHistory(ctx context.Context, in HistoryInput) (*History, error) {
	if !in.Enabled {
		return nil, nil
	}
	if !historyActive(ctx) || in.Root == nil {
		return nil, ErrHistory
	}
	root, err := in.Root.OpenRoot(".")
	if err != nil {
		return nil, ErrHistory
	}
	dir, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, ErrHistory
	}
	h := &History{root: root, dir: dir}
	ok := false
	defer func() {
		if !ok {
			_ = h.Close()
		}
	}()
	info, err := dir.Stat()
	if err != nil || !historyPrivate(info, true) || unix.Flock(int(dir.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil ||
		!historyActive(ctx) {
		return nil, ErrHistory
	}
	if !h.missing(historyIntent) {
		return nil, ErrHistoryUncertain
	}
	if !h.missing(historyStage) {
		return nil, ErrHistory
	}
	if in.Initialize {
		h.state = historyState{Format: historyFormat, Checkpoints: []historyCheckpoint{}, Attempts: []AttemptFacts{}}
		payload, err := json.Marshal(h.state)
		if err != nil {
			return nil, ErrHistory
		}
		if err = h.publish(ctx, payload); err != nil {
			return nil, err
		}
	} else {
		file, err := root.OpenFile(historyFile, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return nil, ErrHistory
		}
		h.currentFD = file // hold through Close or replacement; prevent inode reuse
		info, err := file.Stat()
		if err != nil || !historyPrivate(info, false) || info.Size() < 1 || info.Size() > historyMaxBytes {
			return nil, ErrHistory
		}
		payload, err := io.ReadAll(io.LimitReader(file, historyMaxBytes+1))
		after, statErr := file.Stat()
		if err != nil || statErr != nil || len(payload) > historyMaxBytes || !historySame(info, after) || int64(len(payload)) != info.Size() {
			return nil, ErrHistory
		}
		h.current = after
		if !h.ownsCurrent() || !historyActive(ctx) || !h.missing(historyStage) {
			return nil, ErrHistory
		}
		if h.state, err = decodeHistory(payload); err != nil || !h.available(ctx) {
			return nil, ErrHistory
		}
	}
	ok = true
	return h, nil
}

func historyActive(ctx context.Context) bool { return ctx != nil && ctx.Err() == nil }
func historyPrivate(info os.FileInfo, dir bool) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) &&
		(dir && info.IsDir() && info.Mode().Perm() == 0700 ||
			!dir && info.Mode().IsRegular() && info.Mode().Perm() == 0600 && stat.Nlink == 1)
}
func historySame(expected, current os.FileInfo) bool {
	return historyPrivate(expected, false) && historyPrivate(current, false) && os.SameFile(expected, current) &&
		expected.Size() == current.Size() && expected.ModTime().Equal(current.ModTime())
}
func (h *History) missing(name string) bool {
	_, err := h.root.Lstat(name)
	return errors.Is(err, os.ErrNotExist)
}
func (h *History) ownsCurrent() bool {
	if h.current == nil {
		return h.currentFD == nil && h.missing(historyFile)
	}
	fdInfo, fdErr := h.currentFD.Stat()
	info, err := h.root.Lstat(historyFile)
	return fdErr == nil && err == nil && historySame(h.current, fdInfo) && historySame(fdInfo, info)
}

func (h *History) ownsIntent(intent *os.File) bool {
	expected, err := intent.Stat()
	actual, followErr := h.root.Lstat(historyIntent)
	return err == nil && followErr == nil && historySame(expected, actual) && expected.Size() == 0
}

// publish is called under the single owner's lock. Once the atomic commit
// starts, always finish its durability check. Incomplete staging or uncertain
// publication fences the handle. A durable intent survives replacement until
// the new snapshot is known durable. Neither unresolved file is automatically
// removed or interpreted on restart.
func (h *History) publish(ctx context.Context, payload []byte) error {
	if !historyActive(ctx) || !h.ownsCurrent() || !h.noPending() || !historyActive(ctx) {
		h.failed = true
		return ErrHistory
	}
	file, err := h.root.OpenFile(historyStage, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0600)
	if err != nil {
		h.failed = true
		return ErrHistory
	}
	defer func() {
		if h.currentFD != file {
			_ = file.Close()
		}
	}()
	h.failed = true
	info, err := file.Stat()
	if err != nil || !historyPrivate(info, false) || info.Size() != 0 || !historyActive(ctx) {
		return ErrHistory
	}
	n, err := file.Write(payload)
	if err != nil || n != len(payload) || file.Sync() != nil {
		return ErrHistory
	}
	info, err = file.Stat()
	now, statErr := h.root.Lstat(historyStage)
	if err != nil || statErr != nil || !historySame(info, now) || info.Size() != int64(len(payload)) ||
		!h.ownsCurrent() || !h.missing(historyIntent) || !historyActive(ctx) {
		return ErrHistory
	}
	intent, err := h.root.OpenFile(historyIntent, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return ErrHistory
	}
	defer intent.Close()
	if !h.ownsIntent(intent) || intent.Sync() != nil || h.dir.Sync() != nil {
		return ErrHistory
	}
	now, statErr = h.root.Lstat(historyStage)
	if statErr != nil || !historySame(info, now) || !h.ownsIntent(intent) || !h.ownsCurrent() || !historyActive(ctx) {
		return ErrHistory
	}
	fd := int(h.dir.Fd())
	flags := uint(0)
	if h.current == nil {
		flags = unix.RENAME_NOREPLACE
	}
	if err = unix.Renameat2(fd, historyStage, fd, historyFile, flags); err != nil {
		return ErrHistoryUncertain
	}
	if h.dir.Sync() != nil {
		return ErrHistoryUncertain
	}
	now, err = h.root.Lstat(historyFile)
	if err != nil || !historySame(info, now) || !h.missing(historyStage) || !h.ownsIntent(intent) {
		return ErrHistoryUncertain
	}
	// The snapshot itself is already durable. If cleanup fails, withhold an
	// acknowledgement and leave this handle fenced; absent intent on a later
	// successful open can only reveal a snapshot durably synced above.
	if h.root.Remove(historyIntent) != nil || h.dir.Sync() != nil || !h.noPending() {
		return ErrHistoryUncertain
	}
	if h.currentFD != nil {
		_ = h.currentFD.Close()
	}
	h.currentFD, h.current = file, now
	h.failed = false
	return nil
}

func (h *History) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.root == nil {
		return nil
	}
	var fileErr error
	if h.currentFD != nil {
		fileErr = h.currentFD.Close()
	}
	dirErr := h.dir.Close() // closing the owned descriptor releases its flock
	rootErr := h.root.Close()
	h.dir, h.root, h.state, h.current, h.currentFD = nil, nil, historyState{}, nil, nil
	if dirErr != nil || rootErr != nil || fileErr != nil {
		return ErrHistory
	}
	return nil
}

func historyWall(t time.Time) bool { return validTime(t.UTC()) }
func historyRecordCount(v AttemptFacts) int {
	n := 1
	for _, j := range v.Jobs {
		n += 1 + len(j.Steps)
	}
	return n
}
func historyScope(k AttemptKey) HistoryScope {
	return HistoryScope{k.Source, k.RepositoryID, k.WorkflowID}
}
func historyScopeLess(a, b HistoryScope) bool {
	if a.Source != b.Source {
		return a.Source < b.Source
	}
	if a.RepositoryID != b.RepositoryID {
		return a.RepositoryID < b.RepositoryID
	}
	return a.WorkflowID < b.WorkflowID
}
func historyKeyLess(a, b AttemptKey) bool {
	if x, y := historyScope(a), historyScope(b); x != y {
		return historyScopeLess(x, y)
	}
	if a.RunID != b.RunID {
		return a.RunID < b.RunID
	}
	return a.Attempt < b.Attempt
}
func historyNewer(a, b AttemptFacts) bool {
	if !a.AsOf.Equal(b.AsOf) {
		return a.AsOf.After(b.AsOf)
	}
	return historyKeyLess(b.Key, a.Key)
}

func historyCopyAttempt(in AttemptFacts) (AttemptFacts, error) {
	if _, err := Measure(in); err != nil {
		return AttemptFacts{}, ErrHistory
	}
	copyClock := func(c Clock) Clock {
		c.StartedAt, c.CompletedAt, c.ObservedAt = c.StartedAt.UTC(), c.CompletedAt.UTC(), c.ObservedAt.UTC()
		return c
	}
	out := in
	out.AsOf = out.AsOf.UTC()
	out.Clock = copyClock(out.Clock)
	out.Jobs = make([]JobFacts, len(in.Jobs))
	for i, j := range in.Jobs {
		j.Clock = copyClock(j.Clock)
		j.QueuedAt, j.FirstQueuedObservedAt = j.QueuedAt.UTC(), j.FirstQueuedObservedAt.UTC()
		j.Steps = append(make([]StepFacts, 0, len(j.Steps)), j.Steps...)
		for k := range j.Steps {
			j.Steps[k].Clock = copyClock(j.Steps[k].Clock)
		}
		sort.Slice(j.Steps, func(a, b int) bool { return j.Steps[a].Key.Number < j.Steps[b].Key.Number })
		out.Jobs[i] = j
	}
	sort.Slice(out.Jobs, func(a, b int) bool { return out.Jobs[a].Key.JobID < out.Jobs[b].Key.JobID })
	if _, err := Measure(out); err != nil {
		return AttemptFacts{}, ErrHistory
	}
	return out, nil
}

func decodeHistory(payload []byte) (historyState, error) {
	var s historyState
	if len(payload) == 0 || len(payload) > historyMaxBytes || json.Unmarshal(payload, &s) != nil || s.Format != historyFormat ||
		s.Attempts == nil || s.Checkpoints == nil || len(s.Checkpoints) > historyMaxScopes || len(s.Attempts) > historyMaxRecords {
		return historyState{}, ErrHistory
	}
	scopes := make(map[HistoryScope]bool, len(s.Checkpoints))
	for i, c := range s.Checkpoints {
		if !c.Scope.valid() || c.Cursor.Revision < 1 || c.Cursor.Checkpoint < 0 || c.Digest == ([sha256.Size]byte{}) ||
			i > 0 && !historyScopeLess(s.Checkpoints[i-1].Scope, c.Scope) {
			return historyState{}, ErrHistory
		}
		scopes[c.Scope] = true
	}
	seen := make(map[AttemptKey]bool, len(s.Attempts))
	total := 0
	for i, in := range s.Attempts {
		a, err := historyCopyAttempt(in)
		if err != nil || !scopes[historyScope(a.Key)] || seen[a.Key] || i > 0 && !historyNewer(s.Attempts[i-1], a) {
			return historyState{}, ErrHistory
		}
		total += historyRecordCount(a)
		if total > historyMaxRecords {
			return historyState{}, ErrHistory
		}
		seen[a.Key] = true
		s.Attempts[i] = a
	}
	data, err := json.Marshal(s)
	// Exact canonical equality rejects unknown/duplicate fields and alternate
	// encodings without accepting any extra untyped persisted input.
	if err != nil || !bytes.Equal(payload, data) {
		return historyState{}, ErrHistory
	}
	return s, nil
}

func (h *History) currentCheckpoint(scope HistoryScope) (int, historyCheckpoint) {
	entries := h.state.Checkpoints
	pos := sort.Search(len(entries), func(i int) bool { return !historyScopeLess(entries[i].Scope, scope) })
	if pos < len(entries) && entries[pos].Scope == scope {
		return pos, entries[pos]
	}
	return pos, historyCheckpoint{Scope: scope}
}
func (h *History) available(ctx context.Context) bool {
	if h.root == nil || h.failed || !historyActive(ctx) {
		return false
	}
	if !h.ownsCurrent() || !h.noPending() {
		h.failed = true
		return false
	}
	return historyActive(ctx)
}

func (h *History) noPending() bool {
	return h.missing(historyStage) && h.missing(historyIntent)
}
func (h *History) Cursor(ctx context.Context, scope HistoryScope) (HistoryCursor, error) {
	if h == nil || !scope.valid() {
		return HistoryCursor{}, ErrHistory
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.available(ctx) {
		return HistoryCursor{}, ErrHistory
	}
	_, c := h.currentCheckpoint(scope)
	return c.Cursor, nil
}

// Apply seals a bounded coherent page and its CAS transition as one version.
// It never merges absent children or infers coverage. I/O failure leaves the
// handle fenced without publishing its in-memory cursor. ErrHistoryUncertain
// means the durable outcome may have changed and must not be blindly retried.
// Only the exact most recent accepted transition can be replayed.
func (h *History) Apply(ctx context.Context, scope HistoryScope, expected, next HistoryCursor, page []AttemptFacts, now time.Time) error {
	now = now.UTC()
	cutoff := now.Add(-historyRetention)
	if h == nil || !historyActive(ctx) || !scope.valid() || !historyWall(now) || !historyWall(cutoff) ||
		expected.Revision < 0 || expected.Revision == math.MaxInt64 || expected.Checkpoint < 0 ||
		expected.Revision == 0 && expected.Checkpoint != 0 || next.Revision != expected.Revision+1 || next.Checkpoint < 0 || len(page) > historyMaxPage {
		return ErrHistory
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.available(ctx) {
		return ErrHistory
	}
	records := 0
	attempts := make([]AttemptFacts, 0, len(page))
	seen := make(map[AttemptKey]bool, len(page))
	for _, in := range page {
		if historyScope(in.Key) != scope || seen[in.Key] || in.AsOf.UTC().After(now) || !in.AsOf.UTC().After(cutoff) || !historyActive(ctx) {
			return ErrHistory
		}
		a, err := historyCopyAttempt(in)
		if err != nil {
			return ErrHistory
		}
		records += historyRecordCount(a)
		if records > historyMaxPageRecords {
			return ErrHistory
		}
		seen[a.Key] = true
		attempts = append(attempts, a)
	}
	sort.Slice(attempts, func(i, j int) bool { return historyKeyLess(attempts[i].Key, attempts[j].Key) })
	stamp, err := json.Marshal(struct {
		Scope          HistoryScope
		Expected, Next HistoryCursor
		Attempts       []AttemptFacts
	}{scope, expected, next, attempts})
	if err != nil || len(stamp) > historyMaxBytes {
		return ErrHistory
	}
	digest := sha256.Sum256(stamp)
	pos, actual := h.currentCheckpoint(scope)
	if actual.Cursor == next {
		if actual.Digest == digest {
			return nil
		}
		return ErrHistoryConflict
	}
	if actual.Cursor != expected {
		return ErrHistoryConflict
	}
	if expected.Revision == 0 && len(h.state.Checkpoints) >= historyMaxScopes {
		return ErrHistory
	}
	pending := historyState{Format: historyFormat, Checkpoints: append([]historyCheckpoint{}, h.state.Checkpoints...)}
	if expected.Revision == 0 {
		pending.Checkpoints = append(pending.Checkpoints, historyCheckpoint{})
		copy(pending.Checkpoints[pos+1:], pending.Checkpoints[pos:])
	}
	pending.Checkpoints[pos] = historyCheckpoint{scope, next, digest}
	incoming := make(map[AttemptKey]AttemptFacts, len(attempts))
	for _, a := range attempts {
		incoming[a.Key] = a
	}
	pending.Attempts = make([]AttemptFacts, 0, len(h.state.Attempts)+len(attempts))
	for _, old := range h.state.Attempts {
		if a, ok := incoming[old.Key]; ok {
			if a.AsOf.Before(old.AsOf) {
				return ErrHistoryConflict
			}
			if a.AsOf.Equal(old.AsOf) {
				left, leftErr := json.Marshal(a)
				right, rightErr := json.Marshal(old)
				if leftErr != nil || rightErr != nil || !bytes.Equal(left, right) {
					return ErrHistoryConflict
				}
			}
			continue
		}
		if old.AsOf.After(cutoff) {
			pending.Attempts = append(pending.Attempts, old)
		}
	}
	pending.Attempts = append(pending.Attempts, attempts...)
	sort.Slice(pending.Attempts, func(i, j int) bool { return historyNewer(pending.Attempts[i], pending.Attempts[j]) })
	total := 0
	for i, a := range pending.Attempts {
		total += historyRecordCount(a)
		if total > historyMaxRecords {
			pending.Attempts = pending.Attempts[:i]
			break
		}
	}
	data, err := json.Marshal(pending)
	if err != nil || len(data) > historyMaxBytes {
		return ErrHistory
	}
	if err = h.publish(ctx, data); err != nil {
		return err
	}
	h.state = pending
	return nil
}

func (h *History) Recent(ctx context.Context, s HistoryScope, limit int, now time.Time) ([]AttemptFacts, error) {
	now = now.UTC()
	cutoff := now.Add(-historyRetention)
	if h == nil || !s.valid() || limit < 1 || limit > historyMaxRecent || !historyWall(now) || !historyWall(cutoff) {
		return nil, ErrHistory
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.available(ctx) {
		return nil, ErrHistory
	}
	out := make([]AttemptFacts, 0, limit)
	records := 0
	for _, a := range h.state.Attempts {
		if !a.AsOf.After(cutoff) {
			break
		}
		if a.AsOf.After(now) || historyScope(a.Key) != s {
			continue
		}
		records += historyRecordCount(a)
		if records > historyMaxPageRecords || !historyActive(ctx) {
			return nil, ErrHistory
		}
		row, err := historyCopyAttempt(a)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
		if len(out) == limit {
			break
		}
	}
	if !historyActive(ctx) {
		return nil, ErrHistory
	}
	return out, nil
}
