package adminview

import (
	"errors"
	"sync"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
)

const maxRunnerDiskSnapshotBindings = 64

var errRunnerDiskBindings = errors.New("invalid runner disk snapshot bindings")

type runnerDiskSnapshotKey struct {
	host, pool string
}

type runnerDiskSnapshotSlot struct {
	binding      RunnerDiskHealthBinding
	active       *RunnerDiskHealthAttempt
	reading      RunnerDiskHealthReading
	lastObserved time.Time
	checkedAt    time.Time
	revoked      bool
}

// RunnerDiskHealthAttempt identifies one in-flight sample for the immutable
// reviewed route. A caller must not share it between collection attempts.
type RunnerDiskHealthAttempt struct {
	owner     *RunnerDiskHealthCache
	key       runnerDiskSnapshotKey
	startedAt time.Time
}

// RunnerDiskHealthCache is an optional in-process display source, not a
// collector. It accepts only the configured routes and same-descriptor
// observations accepted by the existing join. It performs no I/O.
type RunnerDiskHealthCache struct {
	mu    sync.Mutex
	slots map[runnerDiskSnapshotKey]*runnerDiskSnapshotSlot
	now   func() time.Time
}

// NewRunnerDiskHealthCache freezes explicitly reviewed startup mappings; it
// cannot discover or replace them. No application code sets it by default.
func NewRunnerDiskHealthCache(bindings []RunnerDiskHealthBinding) (*RunnerDiskHealthCache, error) {
	if len(bindings) == 0 || len(bindings) > maxRunnerDiskSnapshotBindings {
		return nil, errRunnerDiskBindings
	}
	c := &RunnerDiskHealthCache{slots: make(map[runnerDiskSnapshotKey]*runnerDiskSnapshotSlot, len(bindings)), now: time.Now}
	perHost := map[string]int{}
	for _, b := range bindings {
		key := runnerDiskSnapshotKey{b.HostID, b.Scope.StoragePool}
		if !managementText(b.HostID, 256) || !b.Scope.Valid() || b.DirectoryInode == 0 ||
			!runnerDiskWireTime(b.ReviewExpiresAt) || c.slots[key] != nil || perHost[b.HostID] >= maxRunnerDiskHealthPools {
			return nil, errRunnerDiskBindings
		}
		b.ReviewExpiresAt = b.ReviewExpiresAt.UTC()
		c.slots[key] = &runnerDiskSnapshotSlot{binding: b}
		perHost[b.HostID]++
	}
	return c, nil
}

func sameRunnerDiskBinding(a, b RunnerDiskHealthBinding) bool {
	return a.HostID == b.HostID && a.Scope == b.Scope && a.DirectoryInode == b.DirectoryInode &&
		a.ReviewExpiresAt.UTC().Equal(b.ReviewExpiresAt.UTC())
}

func (s *runnerDiskSnapshotSlot) revoke() {
	s.revoked, s.active, s.reading = true, nil, RunnerDiskHealthReading{}
}

// Reviews and renderer timestamps are wall-clock deadlines. UTC strips Go's
// optional monotonic readings, which would otherwise mask a wall-clock rollback
// when two local time.Now() results are compared.
func (c *RunnerDiskHealthCache) wallNow() time.Time { return c.now().UTC() }

func (s *runnerDiskSnapshotSlot) current(now time.Time) bool {
	if s.revoked || !runnerDiskWireTime(now) || !now.Before(s.binding.ReviewExpiresAt) {
		s.revoke()
		return false
	}
	if now.Before(s.checkedAt) {
		// A clock rollback cannot renew an expired review or replay an earlier
		// observation attempt. Do not lower the last observed wall-time bound.
		s.active, s.reading = nil, RunnerDiskHealthReading{}
		return false
	}
	s.checkedAt = now
	if !s.reading.ObservedAt.IsZero() && (s.reading.ObservedAt.After(now) || !now.Before(s.reading.ExpiresAt)) {
		// Keep the observation watermark: expired or withdrawn samples cannot
		// come back even if the process clock later moves backward.
		s.reading = RunnerDiskHealthReading{}
	}
	return true
}

// Begin starts an exact route read. A new attempt supersedes any in-flight
// attempt for this slot. An identity change withdraws and revokes the old
// source; a different independently reviewed mapping needs a new cache.
func (c *RunnerDiskHealthCache) Begin(binding RunnerDiskHealthBinding) (*RunnerDiskHealthAttempt, bool) {
	if c == nil || c.now == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := runnerDiskSnapshotKey{binding.HostID, binding.Scope.StoragePool}
	s := c.slots[key]
	now := c.wallNow()
	if s == nil || !s.current(now) {
		return nil, false
	}
	if !sameRunnerDiskBinding(binding, s.binding) {
		s.revoke()
		return nil, false
	}
	attempt := &RunnerDiskHealthAttempt{owner: c, key: key, startedAt: now}
	s.active = attempt
	return attempt, true
}

// Withdraw removes the exact old route when the producer stops or the route
// moves to another host or pool. A re-enrollment requires independent review
// and a replacement cache; an older in-flight sample cannot republish it.
func (c *RunnerDiskHealthCache) Withdraw(binding RunnerDiskHealthBinding) bool {
	if c == nil || c.now == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.slots[runnerDiskSnapshotKey{binding.HostID, binding.Scope.StoragePool}]
	if s == nil || !sameRunnerDiskBinding(binding, s.binding) {
		return false
	}
	s.revoke()
	return true
}

// Complete consumes the current attempt exactly once. A zero observation
// (including any rejected collection) withdraws old numbers. A retained
// observation showing route identity drift additionally revokes the slot.
func (c *RunnerDiskHealthCache) Complete(attempt *RunnerDiskHealthAttempt, observed hostinventory.BoundDirFilesystemSample) bool {
	return c.complete(attempt, observed.Evidence)
}

// evidence is private; only the opaque sampler's method reaches it outside
// synthetic package tests. This avoids exporting a path for caller counters.
func (c *RunnerDiskHealthCache) complete(attempt *RunnerDiskHealthAttempt, evidence func() (hostinventory.DirFilesystemSample, uint64, time.Time, bool)) bool {
	if c == nil || c.now == nil || attempt == nil || attempt.owner != c {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.slots[attempt.key]
	if s == nil || s.active != attempt {
		return false
	}
	s.active = nil
	// Latest attempt replaces, rather than silently keeping, previous data.
	s.reading = RunnerDiskHealthReading{}
	sample, inode, review, ok := evidence()
	sample.ObservedAt, review = sample.ObservedAt.UTC(), review.UTC()
	now := c.wallNow()
	if !s.current(now) || !ok {
		return false
	}
	if sample.Scope != s.binding.Scope || inode != s.binding.DirectoryInode {
		s.revoke()
		return false
	}
	if !sample.ObservedAt.After(attempt.startedAt) || !sample.ObservedAt.After(s.lastObserved) {
		return false
	}
	host, reading, ok := acceptRunnerDiskHealth(s.binding, sample, inode, review, now)
	if !ok || host != attempt.key.host || reading.StoragePool != attempt.key.pool {
		return false
	}
	s.reading, s.lastObserved = reading, reading.ObservedAt
	return true
}

// SnapshotRunnerDiskHealth returns independent copies of only current
// observations. Rows stay per reviewed route; never sum two pools' filesystem
// counters or use them as budgets. The overview never performs collection.
func (c *RunnerDiskHealthCache) SnapshotRunnerDiskHealth() map[string][]RunnerDiskHealthReading {
	if c == nil || c.now == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.wallNow()
	out := map[string][]RunnerDiskHealthReading{}
	for key, s := range c.slots {
		if s.current(now) && !s.reading.ObservedAt.IsZero() {
			out[key.host] = append(out[key.host], s.reading)
		}
	}
	return out
}
