package cihealth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Bounded, opt-in sidecar retention for verified per-job usage attachments.
// This store is deliberately separate from the timing history format: stock
// v1 timing history stays rollback-compatible and v2 timing snapshots are
// untouched. An attachment carries only the minimized display projection for
// one exact JobKey, the accepted evidence time and the attach time; it never
// stores credentials, raw samples, host identities, job output or commands.
// Nothing here authorizes sizing, enforcement, dispatch or any mutation: the
// attachment is a read-only display fact with explicit unavailable/stale
// states preserved exactly as verified.

const (
	usageAttachmentFile      = "ci-job-usage-v2.json"
	usageAttachmentStage     = "ci-job-usage-v2.stage"
	usageAttachmentFormat    = "garm-ci-job-usage-v2"
	usageAttachmentRetention = 30 * 24 * time.Hour
	usageAttachmentMaxRecord = 4096
	usageAttachmentMaxBytes  = 4 << 20
	usageAttachmentMaxScopes = 64
)

var (
	ErrUsageAttachment         = errors.New("CI job usage attachment unavailable")
	ErrUsageAttachmentConflict = errors.New("CI job usage attachment conflicts with retained evidence")
)

// UsageAttachmentScope is the enrollment unit for attachments: one exact
// configured source and repository. Enrollment is authorized before any
// storage lookup; revocation immediately prevents a retained attachment from
// being read, without rewriting or deleting retained history.
type UsageAttachmentScope struct {
	Source       string `json:"source"`
	RepositoryID int64  `json:"repository_id"`
}

func (s UsageAttachmentScope) valid() bool {
	return validSource(s.Source) && s.RepositoryID > 0
}

func usageScopeOf(job JobKey) UsageAttachmentScope {
	return UsageAttachmentScope{Source: job.Attempt.Source, RepositoryID: job.Attempt.RepositoryID}
}

// UsageAttachment is the retained minimized projection for one exact job.
// EvidenceAsOf is the accepted source observation time; AttachedAt is when
// the controller accepted the attachment. Neither is ever refreshed by a
// read, a replay or a restart.
type UsageAttachment struct {
	Job          JobKey    `json:"job"`
	Evidence     Wire      `json:"evidence"`
	RunnerDigest string    `json:"runner_digest"`
	InputDigest  string    `json:"input_digest"`
	WindowStart  time.Time `json:"window_start"`
	WindowEnd    time.Time `json:"window_end"`
	EvidenceAsOf time.Time `json:"evidence_as_of"`
	Integrity    string    `json:"integrity"`
	AttachedAt   time.Time `json:"attached_at"`
}

type usageAttachmentState struct {
	Format      string                 `json:"format"`
	Enrolled    []UsageAttachmentScope `json:"enrolled"`
	Attachments []UsageAttachment      `json:"attachments"`
}

type UsageAttachmentInput struct {
	Enabled bool
	History *History // Independently owned, current exact-attempt membership.
	// Root must already be under this deployment's uninterrupted exclusive
	// service-user custody, exactly like the timing history directory. The
	// store duplicates the directory descriptor; no path is accepted.
	Root *os.Root
	// Initialize publishes the empty store only if the exact slot is absent;
	// otherwise an existing valid snapshot is required.
	Initialize bool
}

// UsageAttachmentStore is the single-owner bounded sidecar. Flock excludes a
// second cooperating opener of the same directory; uncertain publication
// fences the handle instead of guessing.
type UsageAttachmentStore struct {
	mu        sync.Mutex
	root      *os.Root
	dir       *os.File
	currentFD *os.File
	current   os.FileInfo
	state     usageAttachmentState
	failed    bool
	history   *History
	matrix    AttributionCapabilityMatrix
	now       func() time.Time
	wake      chan struct{}
	stop      chan struct{}
	done      chan struct{}
}

func OpenUsageAttachments(ctx context.Context, in UsageAttachmentInput) (*UsageAttachmentStore, error) {
	return openUsageAttachments(ctx, in, DocumentedMatrix(), time.Now, time.Minute)
}

func openUsageAttachments(ctx context.Context, in UsageAttachmentInput, matrix AttributionCapabilityMatrix, clock func() time.Time, maxWait time.Duration) (*UsageAttachmentStore, error) {
	if !in.Enabled {
		return nil, nil
	}
	if !historyActive(ctx) || in.Root == nil || in.History == nil || !matrix.supportsExactJobAttribution() || maxWait <= 0 || maxWait > time.Minute || clock == nil || !validTime(clock()) {
		return nil, ErrUsageAttachment
	}
	root, err := in.Root.OpenRoot(".")
	if err != nil {
		return nil, ErrUsageAttachment
	}
	dir, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, ErrUsageAttachment
	}
	s := &UsageAttachmentStore{root: root, dir: dir, history: in.History, matrix: matrix, now: clock}
	ok := false
	defer func() {
		if !ok {
			_ = s.Close()
		}
	}()
	info, err := dir.Stat()
	if err != nil || !historyPrivate(info, true) || unix.Flock(int(dir.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil ||
		!historyActive(ctx) || !s.missing(usageAttachmentStage) {
		return nil, ErrUsageAttachment
	}
	if in.Initialize {
		s.state = usageAttachmentState{Format: usageAttachmentFormat, Enrolled: []UsageAttachmentScope{}, Attachments: []UsageAttachment{}}
		payload, err := json.Marshal(s.state)
		if err != nil {
			return nil, ErrUsageAttachment
		}
		if err = s.publish(ctx, payload); err != nil {
			return nil, err
		}
	} else {
		file, err := root.OpenFile(usageAttachmentFile, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return nil, ErrUsageAttachment
		}
		s.currentFD = file
		info, err := file.Stat()
		if err != nil || !historyPrivate(info, false) || info.Size() < 1 || info.Size() > usageAttachmentMaxBytes {
			return nil, ErrUsageAttachment
		}
		payload, err := io.ReadAll(io.LimitReader(file, usageAttachmentMaxBytes+1))
		after, statErr := file.Stat()
		if err != nil || statErr != nil || len(payload) > usageAttachmentMaxBytes || !historySame(info, after) || int64(len(payload)) != info.Size() {
			return nil, ErrUsageAttachment
		}
		s.current = after
		if !s.ownsCurrent() || !historyActive(ctx) {
			return nil, ErrUsageAttachment
		}
		if s.state, err = decodeUsageAttachments(payload, s.now().UTC(), matrix); err != nil || !s.available(ctx) {
			return nil, ErrUsageAttachment
		}
	}
	if err := s.prune(ctx, s.now().UTC()); err != nil {
		return nil, err
	}
	s.stop, s.done, s.wake = make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	go s.maintain(maxWait)
	ok = true
	return s, nil
}

func (s *UsageAttachmentStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer func() {
		s.mu.Unlock()
		if s.done != nil {
			<-s.done
		}
	}()
	if s.root == nil {
		return nil
	}
	if s.stop != nil {
		close(s.stop)
	}
	var fileErr error
	if s.currentFD != nil {
		fileErr = s.currentFD.Close()
	}
	dirErr := s.dir.Close()
	rootErr := s.root.Close()
	s.dir, s.root, s.current, s.currentFD = nil, nil, nil, nil
	s.state = usageAttachmentState{}
	if dirErr != nil || rootErr != nil || fileErr != nil {
		return ErrUsageAttachment
	}
	return nil
}

func (s *UsageAttachmentStore) missing(name string) bool {
	_, err := s.root.Lstat(name)
	return errors.Is(err, os.ErrNotExist)
}

func (s *UsageAttachmentStore) ownsCurrent() bool {
	if s.current == nil {
		return s.currentFD == nil && s.missing(usageAttachmentFile)
	}
	fdInfo, fdErr := s.currentFD.Stat()
	info, err := s.root.Lstat(usageAttachmentFile)
	return fdErr == nil && err == nil && historySame(s.current, fdInfo) && historySame(fdInfo, info)
}

func (s *UsageAttachmentStore) available(ctx context.Context) bool {
	if s.root == nil || s.failed || !historyActive(ctx) {
		return false
	}
	if !s.ownsCurrent() || !s.missing(usageAttachmentStage) {
		s.failed = true
		return false
	}
	return historyActive(ctx)
}

// publish atomically replaces the snapshot with stage+rename and directory
// fsync, mirroring the reviewed timing-history discipline. An incomplete
// stage is never interpreted on restart; the handle fences instead.
func (s *UsageAttachmentStore) publish(ctx context.Context, payload []byte) error {
	if !historyActive(ctx) || !s.ownsCurrent() || !s.missing(usageAttachmentStage) || !historyActive(ctx) {
		s.failed = true
		return ErrUsageAttachment
	}
	file, err := s.root.OpenFile(usageAttachmentStage, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0600)
	if err != nil {
		s.failed = true
		return ErrUsageAttachment
	}
	defer func() {
		if s.currentFD != file {
			_ = file.Close()
		}
	}()
	s.failed = true
	info, err := file.Stat()
	if err != nil || !historyPrivate(info, false) || info.Size() != 0 || !historyActive(ctx) {
		return ErrUsageAttachment
	}
	n, err := file.Write(payload)
	if err != nil || n != len(payload) || file.Sync() != nil {
		return ErrUsageAttachment
	}
	info, err = file.Stat()
	now, statErr := s.root.Lstat(usageAttachmentStage)
	if err != nil || statErr != nil || !historySame(info, now) || info.Size() != int64(len(payload)) ||
		!s.ownsCurrent() || !historyActive(ctx) {
		return ErrUsageAttachment
	}
	fd := int(s.dir.Fd())
	flags := uint(0)
	if s.current == nil {
		flags = unix.RENAME_NOREPLACE
	}
	if err = unix.Renameat2(fd, usageAttachmentStage, fd, usageAttachmentFile, flags); err != nil {
		return ErrUsageAttachment
	}
	if s.dir.Sync() != nil {
		return ErrUsageAttachment
	}
	now, err = s.root.Lstat(usageAttachmentFile)
	if err != nil || !historySame(info, now) || !s.missing(usageAttachmentStage) {
		return ErrUsageAttachment
	}
	if s.currentFD != nil {
		_ = s.currentFD.Close()
	}
	s.currentFD, s.current = file, now
	s.failed = false
	return nil
}

func validUsageAttachmentScopeList(scopes []UsageAttachmentScope) bool {
	if scopes == nil || len(scopes) > usageAttachmentMaxScopes {
		return false
	}
	for i, scope := range scopes {
		if !scope.valid() || i > 0 && !usageScopeLess(scopes[i-1], scope) {
			return false
		}
	}
	return true
}

func usageScopeLess(a, b UsageAttachmentScope) bool {
	if a.Source != b.Source {
		return a.Source < b.Source
	}
	return a.RepositoryID < b.RepositoryID
}

func usageJobLess(a, b JobKey) bool {
	if a.Attempt != b.Attempt {
		return historyKeyLess(a.Attempt, b.Attempt)
	}
	return a.JobID < b.JobID
}

func usageDigest(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (a UsageAttachment) checksum() string {
	a.Integrity = ""
	return usageDigest(a)
}

func usageQuantity(s string, bits int) (uint64, bool) {
	n, err := strconv.ParseUint(s, 10, bits)
	return n, err == nil && strconv.FormatUint(n, 10) == s
}

func usageCoverage(s string, complete bool, window time.Duration, samples int) bool {
	n, ok := usageQuantity(s, 63)
	return ok && samples >= 2 && n > 0 && n <= uint64(window) && complete == (n == uint64(window))
}

func validUsageDisk(d DiskWire, a UsageAttachment, window time.Duration, samples int, matrix AttributionCapabilityMatrix) bool {
	if d.State == VerifiedUsageUnavailable {
		return (d.Reason == UsageOmissionDiskUnmeasured || d.Reason == UsageOmissionDiskUnsupported && !matrix.Supports(FactDiskCounters)) &&
			d.Version == "" && d.Read == nil && d.Write == nil && d.Used == nil
	}
	if !matrix.Supports(FactDiskCounters) || d.State != VerifiedUsageVerified || d.Reason != "" ||
		d.Version != DiskWireVersion || (d.Read == nil && d.Write == nil && d.Used == nil) {
		return false
	}
	for _, c := range []*DiskCounterWire{d.Read, d.Write} {
		if c != nil {
			if _, ok := usageQuantity(c.Bytes, 64); !ok || !usageCoverage(c.CoveredNanoseconds, c.Complete, window, samples) {
				return false
			}
		}
	}
	g := d.Used
	if g == nil {
		return true
	}
	_, ok := usageQuantity(g.SamplePeakBytes, 64)
	return ok && g.GaugeSamples >= 1 && g.GaugeSamples <= samples && usageWireTime(g.PeakAt) && usageWireTime(g.LatestSampleAt) &&
		!g.PeakAt.Before(a.WindowStart) && !g.LatestSampleAt.Before(g.PeakAt) && !g.LatestSampleAt.After(a.WindowEnd) &&
		(g.GaugeSamples != 1 || g.PeakAt.Equal(g.LatestSampleAt))
}

// The CI Health browser admits UTC Z timestamps and signed-64 CPU/RAM
// quantities. Disk byte quantities have their own unsigned-64 wire range.
func usageWireTime(at time.Time) bool { return validTime(at) && at.Location() == time.UTC }

func (a UsageAttachment) valid(now time.Time, matrix AttributionCapabilityMatrix) bool {
	if !validKey(a.Job.Attempt) || a.Job.JobID < 1 || !stockJobDigest(a.RunnerDigest) || !stockJobDigest(a.InputDigest) ||
		!stockJobDigest(a.Integrity) || a.Integrity != a.checksum() || !validTime(a.AttachedAt) || !validTime(a.EvidenceAsOf) ||
		a.AttachedAt.After(now) || a.EvidenceAsOf.After(a.AttachedAt) || a.Evidence.AsOf == nil || !usageWireTime(*a.Evidence.AsOf) || !a.EvidenceAsOf.Equal(*a.Evidence.AsOf) ||
		!validTime(a.WindowStart) || !validTime(a.WindowEnd) || !a.WindowEnd.After(a.WindowStart) || a.WindowEnd.After(a.EvidenceAsOf) {
		return false
	}
	window := a.WindowEnd.Sub(a.WindowStart)
	if window > MaxJobResourceWindow {
		return false
	}
	e, m := a.Evidence, a.Evidence.Measured
	// Only records derived after positive job, runner and bracket verification
	// can be retained, including an uncaptured job with no measurements.
	if e.State == VerifiedUsageStale {
		return m == nil && e.Reason == UsageOmissionStaleEvidence
	}
	if e.State == VerifiedUsageUnavailable {
		return m == nil && e.Reason == UsageOmissionJobTooShort
	}
	if (e.State != VerifiedUsageVerified && e.State != VerifiedUsagePartial) || e.Reason != "" || m == nil ||
		m.Window != strconv.FormatInt(int64(window), 10) || m.Samples < 1 || m.Samples > MaxJobResourceSamples ||
		!validUsageDisk(m.Disk, a, window, m.Samples, matrix) {
		return false
	}
	if c := m.CPU; c != nil {
		mean, ok := usageQuantity(c.AverageMilliCPU, 63)
		peak, okPeak := usageQuantity(c.PeakMilliCPU, 63)
		if !matrix.Supports(FactCPUCounters) || !ok || !okPeak || mean > peak || !usageCoverage(c.CoveredNanoseconds, c.Complete, window, m.Samples) ||
			c.MeanBytes != "" || c.SamplePeakBytes != "" || c.GaugeSamples != 0 {
			return false
		}
	}
	if r := m.RAM; r != nil {
		if !matrix.Supports(FactRAMCounters) || r.AverageMilliCPU != "" || r.PeakMilliCPU != "" || r.SamplePeakBytes == "" {
			return false
		}
		peak, ok := usageQuantity(r.SamplePeakBytes, 63)
		if !ok || r.GaugeSamples < 1 || r.GaugeSamples > m.Samples {
			return false
		}
		if r.MeanBytes == "" {
			if r.CoveredNanoseconds != "" || r.Complete {
				return false
			}
		} else {
			mean, ok := usageQuantity(r.MeanBytes, 63)
			if !ok || mean > peak || !usageCoverage(r.CoveredNanoseconds, r.Complete, window, r.GaugeSamples) {
				return false
			}
		}
	}
	full := m.CPU != nil && m.CPU.Complete && m.RAM != nil && m.RAM.Complete
	return (e.State == VerifiedUsageVerified) == full && (m.CPU != nil || m.RAM != nil || m.Disk.State == VerifiedUsageVerified)
}

func decodeUsageAttachments(payload []byte, now time.Time, matrix AttributionCapabilityMatrix) (usageAttachmentState, error) {
	var s usageAttachmentState
	if len(payload) == 0 || len(payload) > usageAttachmentMaxBytes || json.Unmarshal(payload, &s) != nil ||
		s.Format != usageAttachmentFormat || !validUsageAttachmentScopeList(s.Enrolled) ||
		s.Attachments == nil || len(s.Attachments) > usageAttachmentMaxRecord {
		return usageAttachmentState{}, ErrUsageAttachment
	}
	for i, a := range s.Attachments {
		if !a.valid(now, matrix) || i > 0 && !usageJobLess(s.Attachments[i-1].Job, a.Job) {
			return usageAttachmentState{}, ErrUsageAttachment
		}
	}
	data, err := json.Marshal(s)
	if err != nil || !bytes.Equal(payload, data) {
		return usageAttachmentState{}, ErrUsageAttachment
	}
	return s, nil
}

func (s *UsageAttachmentStore) enrolled(scope UsageAttachmentScope) bool {
	pos := sort.Search(len(s.state.Enrolled), func(i int) bool { return !usageScopeLess(s.state.Enrolled[i], scope) })
	return pos < len(s.state.Enrolled) && s.state.Enrolled[pos] == scope
}

func (s *UsageAttachmentStore) commit(ctx context.Context, next usageAttachmentState) error {
	data, err := json.Marshal(next)
	if err != nil || len(data) > usageAttachmentMaxBytes {
		return ErrUsageAttachment
	}
	if err := s.publish(ctx, data); err != nil {
		return err
	}
	s.state = next
	return nil
}

// SetEnrolled durably authorizes or revokes one exact scope. Revocation takes
// effect for every later read in the same commit; retained attachments are
// not rewritten, so an honest re-enrollment sees the original old evidence
// times, never refreshed ones.
func (s *UsageAttachmentStore) SetEnrolled(ctx context.Context, scope UsageAttachmentScope, enrolled bool) error {
	if s == nil || !scope.valid() {
		return ErrUsageAttachment
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.available(ctx) {
		return ErrUsageAttachment
	}
	if s.enrolled(scope) == enrolled {
		return nil
	}
	next := usageAttachmentState{Format: s.state.Format, Enrolled: append([]UsageAttachmentScope{}, s.state.Enrolled...), Attachments: s.state.Attachments}
	if enrolled {
		if len(next.Enrolled) >= usageAttachmentMaxScopes {
			return ErrUsageAttachment
		}
		pos := sort.Search(len(next.Enrolled), func(i int) bool { return !usageScopeLess(next.Enrolled[i], scope) })
		next.Enrolled = append(next.Enrolled, UsageAttachmentScope{})
		copy(next.Enrolled[pos+1:], next.Enrolled[pos:])
		next.Enrolled[pos] = scope
	} else {
		pos := sort.Search(len(next.Enrolled), func(i int) bool { return !usageScopeLess(next.Enrolled[i], scope) })
		if pos >= len(next.Enrolled) || next.Enrolled[pos] != scope {
			return ErrUsageAttachment
		}
		next.Enrolled = append(next.Enrolled[:pos], next.Enrolled[pos+1:]...)
	}
	return s.commit(ctx, next)
}

// Callers hold the sidecar mutex followed by the timing history mutex. The
// latest version alone defines row membership. A future version after a clock
// regression also blocks reads, even if an older matching row exists.
func (s *UsageAttachmentStore) currentJobClock(ctx context.Context, job JobKey, now time.Time) (Clock, time.Time, bool) {
	if !s.enrolled(usageScopeOf(job)) || !s.history.available(ctx) || !s.available(ctx) {
		return Clock{}, time.Time{}, false
	}
	for _, attempt := range s.history.state.Attempts {
		if attempt.Key != job.Attempt {
			continue
		}
		if attempt.AsOf.After(now) || !attempt.AsOf.After(now.Add(-historyRetention)) {
			return Clock{}, time.Time{}, false
		}
		for _, row := range attempt.Jobs {
			if row.Key == job {
				return row.Clock, attempt.AsOf, true
			}
		}
		return Clock{}, time.Time{}, false
	}
	return Clock{}, time.Time{}, false
}

// A sparse latest row can establish membership without measuring execution.
// Positive endpoints on that row must agree with the historical usage window.
func timingAllowsUsageWindow(row Clock, start, end time.Time) bool {
	return (row.StartedAt.IsZero() || row.StartedAt.Equal(start)) && (row.CompletedAt.IsZero() || !row.CompletedAt.Before(end))
}

func (s *UsageAttachmentStore) position(job JobKey) (int, bool) {
	rows := s.state.Attachments
	pos := sort.Search(len(rows), func(i int) bool { return !usageJobLess(rows[i].Job, job) })
	return pos, pos < len(rows) && rows[pos].Job == job
}

// Attach builds the display evidence internally from the exact verified
// attribution and bounded sample window. No caller-supplied wire is accepted.
// Replaying identical validated input retains its original evidence and
// attachment times, including after a later read or restart. A later
// observation may update only the same runner and measured window.
func (s *UsageAttachmentStore) Attach(ctx context.Context, claim JobAttributionClaim, set VerifiedUsageSampleSet) error {
	if s == nil || len(set.Samples) > MaxJobResourceSamples {
		return ErrUsageAttachment
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil || s.history == nil || s.now == nil {
		return ErrUsageAttachment
	}
	s.history.mu.Lock()
	defer s.history.mu.Unlock()
	now := s.now().UTC()
	row, _, member := s.currentJobClock(ctx, claim.Job, now)
	if !validTime(now) || !member {
		return ErrUsageAttachment
	}
	if !timingAllowsUsageWindow(row, set.Scope.StartedAt, set.Scope.EndAt) {
		return ErrUsageAttachmentConflict
	}
	if err := s.prune(ctx, now); err != nil {
		return err
	}
	a, err := VerifyJobAttribution(s.matrix, claim, now)
	if err != nil || classifySampleSet(a, set, now).reason != UsageOmissionNone {
		return ErrUsageAttachment
	}
	inputDigest := usageDigest(struct {
		Claim   JobAttributionClaim
		Samples VerifiedUsageSampleSet
	}{claim, set})
	if !stockJobDigest(inputDigest) {
		return ErrUsageAttachment
	}
	pos, exists := s.position(a.Job)
	if exists {
		old := s.state.Attachments[pos]
		if old.AttachedAt.After(now) {
			return ErrUsageAttachment
		}
		if old.InputDigest == inputDigest {
			return nil
		}
	}
	record := BuildVerifiedJobUsage(s.matrix, claim, set, now)
	if record.AsOf.After(now) || !record.AsOf.After(now.Add(-usageAttachmentRetention)) {
		return ErrUsageAttachment
	}
	runner := usageDigest(struct {
		Job        JobKey
		Enrollment EnrolledRowScope
		Row        string
		Agent      int64
		Runtime    StockRunnerIncarnation
		CreatedAt  time.Time
	}{a.Job, a.Enrolled, a.RowID, a.AgentID, a.Incarnation, a.Origin.InstanceCreatedAt.UTC()})
	next := UsageAttachment{Job: a.Job, RunnerDigest: runner, InputDigest: inputDigest,
		WindowStart: set.Scope.StartedAt.UTC(), WindowEnd: set.Scope.EndAt.UTC(),
		Evidence: record.Wire(s.matrix), EvidenceAsOf: record.AsOf.UTC(), AttachedAt: now}
	next.Integrity = next.checksum()
	if !next.valid(now, s.matrix) {
		return ErrUsageAttachment
	}
	rows := append([]UsageAttachment{}, s.state.Attachments...)
	if exists {
		old := rows[pos]
		if old.RunnerDigest != runner || !old.WindowStart.Equal(next.WindowStart) ||
			!old.WindowEnd.Equal(next.WindowEnd) || !old.EvidenceAsOf.Before(next.EvidenceAsOf) {
			return ErrUsageAttachmentConflict
		}
		rows[pos] = next
	} else {
		if len(rows) >= usageAttachmentMaxRecord {
			return ErrUsageAttachment
		}
		rows = append(rows, UsageAttachment{})
		copy(rows[pos+1:], rows[pos:])
		rows[pos] = next
	}
	state := s.state
	state.Attachments = rows
	if err := s.commit(ctx, state); err != nil {
		return err
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

func (s *UsageAttachmentStore) Read(ctx context.Context, job JobKey) (UsageAttachment, bool, error) {
	if s == nil || !validKey(job.Attempt) || job.JobID <= 0 {
		return UsageAttachment{}, false, ErrUsageAttachment
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil || s.history == nil || s.now == nil {
		return UsageAttachment{}, false, ErrUsageAttachment
	}
	s.history.mu.Lock()
	defer s.history.mu.Unlock()
	now := s.now().UTC()
	a, _, _, err := s.readCurrent(ctx, job, now)
	if err != nil || a.Job != job {
		return UsageAttachment{}, false, err
	}
	raw, err := json.Marshal(a)
	var out UsageAttachment
	if err != nil || json.Unmarshal(raw, &out) != nil {
		return UsageAttachment{}, false, ErrUsageAttachment
	}
	return out, true, nil
}

// Callers hold both the sidecar and timing locks. The returned attachment
// is owned by the store and must be copied before crossing that boundary.
func (s *UsageAttachmentStore) readCurrent(ctx context.Context, job JobKey, now time.Time) (UsageAttachment, Clock, time.Time, error) {
	row, membershipAt, member := s.currentJobClock(ctx, job, now)
	if !validTime(now) || !member {
		return UsageAttachment{}, Clock{}, time.Time{}, ErrUsageAttachment
	}
	pos, exists := s.position(job)
	if !exists {
		return UsageAttachment{}, Clock{}, time.Time{}, nil
	}
	a := s.state.Attachments[pos]
	if !timingAllowsUsageWindow(row, a.WindowStart, a.WindowEnd) {
		return UsageAttachment{}, Clock{}, time.Time{}, ErrUsageAttachmentConflict
	}
	if a.AttachedAt.After(now) {
		return UsageAttachment{}, Clock{}, time.Time{}, ErrUsageAttachment
	}
	if !a.EvidenceAsOf.After(now.Add(-usageAttachmentRetention)) {
		return UsageAttachment{}, Clock{}, time.Time{}, nil
	}
	return a, row, membershipAt.Add(historyRetention), nil
}

func (s *UsageAttachmentStore) prune(ctx context.Context, now time.Time) error {
	if !validTime(now) || !s.available(ctx) {
		return ErrUsageAttachment
	}
	cutoff := now.Add(-usageAttachmentRetention)
	next := s.state
	next.Attachments = make([]UsageAttachment, 0, len(s.state.Attachments))
	for _, a := range s.state.Attachments {
		if a.EvidenceAsOf.After(cutoff) {
			next.Attachments = append(next.Attachments, a)
		}
	}
	if len(next.Attachments) == len(s.state.Attachments) {
		return nil
	}
	return s.commit(ctx, next)
}

// The owner wakes at the earliest evidence expiry, with a bounded polling
// interval to handle wall-clock adjustments even without attachment traffic.
func (s *UsageAttachmentStore) maintain(maxWait time.Duration) {
	defer close(s.done)
	for {
		s.mu.Lock()
		if s.root == nil {
			s.mu.Unlock()
			return
		}
		now := s.now().UTC()
		if err := s.prune(context.Background(), now); err != nil {
			s.failed = true
			s.mu.Unlock()
			return
		}
		wait := maxWait
		for _, a := range s.state.Attachments {
			wait = min(wait, a.EvidenceAsOf.Add(usageAttachmentRetention).Sub(now))
		}
		s.mu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-s.stop:
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}
