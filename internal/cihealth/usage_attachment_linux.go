package cihealth

import (
	"bytes"
	"context"
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
	usageAttachmentFile      = "ci-job-usage-v1.json"
	usageAttachmentStage     = "ci-job-usage-v1.stage"
	usageAttachmentFormat    = "garm-ci-job-usage-v1"
	usageAttachmentRetention = 30 * 24 * time.Hour
	usageAttachmentLookback  = 30 * 24 * time.Hour
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
	EvidenceAsOf time.Time `json:"evidence_as_of"`
	AttachedAt   time.Time `json:"attached_at"`
}

type usageAttachmentState struct {
	Format      string                 `json:"format"`
	Enrolled    []UsageAttachmentScope `json:"enrolled"`
	Attachments []UsageAttachment      `json:"attachments"`
}

type UsageAttachmentInput struct {
	Enabled bool
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
}

func OpenUsageAttachments(ctx context.Context, in UsageAttachmentInput) (*UsageAttachmentStore, error) {
	if !in.Enabled {
		return nil, nil
	}
	if !historyActive(ctx) || in.Root == nil {
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
	s := &UsageAttachmentStore{root: root, dir: dir}
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
		if s.state, err = decodeUsageAttachments(payload); err != nil || !s.available(ctx) {
			return nil, ErrUsageAttachment
		}
	}
	ok = true
	return s, nil
}

func (s *UsageAttachmentStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		return nil
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
	if a.Attempt.Source != b.Attempt.Source {
		return a.Attempt.Source < b.Attempt.Source
	}
	for _, pair := range [...][2]int64{
		{a.Attempt.RepositoryID, b.Attempt.RepositoryID},
		{a.Attempt.WorkflowID, b.Attempt.WorkflowID},
		{a.Attempt.RunID, b.Attempt.RunID},
		{a.Attempt.Attempt, b.Attempt.Attempt},
		{a.JobID, b.JobID},
	} {
		if pair[0] != pair[1] {
			return pair[0] < pair[1]
		}
	}
	return false
}

// validUsageAttachmentEvidence bounds one attachment before it is retained:
// the exact job identity, a valid display projection whose measured block is
// present exactly for verified/partial states, and an honest evidence time.
// Old evidence is retained as old; nothing here refreshes it.
func validUsageAttachmentEvidence(a UsageAttachment, now time.Time) bool {
	if !validKey(a.Job.Attempt) || a.Job.JobID <= 0 || !validTime(a.EvidenceAsOf.UTC()) || !validTime(a.AttachedAt.UTC()) ||
		a.EvidenceAsOf.UTC().After(now.UTC()) || a.AttachedAt.UTC().After(now.UTC()) || a.EvidenceAsOf.UTC().After(a.AttachedAt.UTC()) ||
		now.UTC().Sub(a.EvidenceAsOf.UTC()) > usageAttachmentLookback {
		return false
	}
	e := a.Evidence
	if e.State != VerifiedUsageUnavailable && e.State != VerifiedUsageStale &&
		e.State != VerifiedUsagePartial && e.State != VerifiedUsageVerified {
		return false
	}
	switch e.State {
	case VerifiedUsageUnavailable, VerifiedUsageStale:
		if e.Reason == UsageOmissionNone || e.Measured != nil {
			return false
		}
	default:
		if e.Reason != UsageOmissionNone || e.Measured == nil {
			return false
		}
		window, err := strconv.ParseUint(e.Measured.Window, 10, 64)
		if err != nil || e.Measured.Samples < 1 || e.Measured.Samples > MaxJobResourceSamples ||
			window == 0 || window > uint64(MaxJobResourceWindow) {
			return false
		}
	}
	if e.AsOf != nil && (!validTime(e.AsOf.UTC()) || e.AsOf.UTC().After(now.UTC())) {
		return false
	}
	return true
}

func decodeUsageAttachments(payload []byte) (usageAttachmentState, error) {
	var s usageAttachmentState
	if len(payload) == 0 || len(payload) > usageAttachmentMaxBytes || json.Unmarshal(payload, &s) != nil ||
		s.Format != usageAttachmentFormat || !validUsageAttachmentScopeList(s.Enrolled) ||
		s.Attachments == nil || len(s.Attachments) > usageAttachmentMaxRecord {
		return usageAttachmentState{}, ErrUsageAttachment
	}
	for i, a := range s.Attachments {
		if !validKey(a.Job.Attempt) || a.Job.JobID <= 0 || !validTime(a.EvidenceAsOf.UTC()) || !validTime(a.AttachedAt.UTC()) ||
			a.EvidenceAsOf.UTC().After(a.AttachedAt.UTC()) || i > 0 && !usageJobLess(s.Attachments[i-1].Job, a.Job) {
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

// Attach retains one verified usage attachment for its exact job. The scope
// must be enrolled before any storage lookup. A byte-identical replay of the
// retained attachment is a no-op; a conflicting payload for the same exact
// job, or older evidence replacing newer, is refused and changes nothing.
// Retention pruning removes only expired records; capacity pressure beyond
// the bound refuses the write instead of evicting unexpired evidence.
func (s *UsageAttachmentStore) Attach(ctx context.Context, a UsageAttachment, now time.Time) error {
	now = now.UTC()
	if s == nil || !historyWall(now) {
		return ErrUsageAttachment
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.available(ctx) || !s.enrolled(usageScopeOf(a.Job)) || !validUsageAttachmentEvidence(a, now) {
		return ErrUsageAttachment
	}
	incoming, err := json.Marshal(a)
	if err != nil {
		return ErrUsageAttachment
	}
	cutoff := now.Add(-usageAttachmentRetention)
	next := usageAttachmentState{Format: s.state.Format, Enrolled: s.state.Enrolled, Attachments: make([]UsageAttachment, 0, len(s.state.Attachments)+1)}
	replayed := false
	for _, old := range s.state.Attachments {
		if old.Job == a.Job {
			retained, err := json.Marshal(old)
			if err != nil {
				return ErrUsageAttachment
			}
			if bytes.Equal(retained, incoming) {
				replayed = true
			} else if !old.EvidenceAsOf.UTC().Before(a.EvidenceAsOf.UTC()) {
				return ErrUsageAttachmentConflict
			} else {
				continue
			}
		}
		if old.EvidenceAsOf.UTC().After(cutoff) {
			next.Attachments = append(next.Attachments, old)
		}
	}
	if replayed {
		return nil
	}
	if len(next.Attachments) >= usageAttachmentMaxRecord {
		return ErrUsageAttachment
	}
	next.Attachments = append(next.Attachments, a)
	sort.Slice(next.Attachments, func(i, j int) bool { return usageJobLess(next.Attachments[i].Job, next.Attachments[j].Job) })
	return s.commit(ctx, next)
}

// Read returns the retained attachment for one exact job only while its
// scope stays enrolled and the evidence is inside retention. Sibling
// attempts, sibling jobs, foreign repositories and revoked scopes all fail
// closed to unavailable; an absent attachment is reported as absent, never
// as a zero measurement.
func (s *UsageAttachmentStore) Read(ctx context.Context, job JobKey, now time.Time) (UsageAttachment, bool, error) {
	now = now.UTC()
	if s == nil || !validKey(job.Attempt) || job.JobID <= 0 || !historyWall(now) {
		return UsageAttachment{}, false, ErrUsageAttachment
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.available(ctx) || !s.enrolled(usageScopeOf(job)) {
		return UsageAttachment{}, false, ErrUsageAttachment
	}
	cutoff := now.Add(-usageAttachmentRetention)
	pos := sort.Search(len(s.state.Attachments), func(i int) bool { return !usageJobLess(s.state.Attachments[i].Job, job) })
	if pos >= len(s.state.Attachments) || s.state.Attachments[pos].Job != job {
		return UsageAttachment{}, false, nil
	}
	a := s.state.Attachments[pos]
	if !a.EvidenceAsOf.UTC().After(cutoff) {
		return UsageAttachment{}, false, nil
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return UsageAttachment{}, false, ErrUsageAttachment
	}
	var out UsageAttachment
	if json.Unmarshal(raw, &out) != nil {
		return UsageAttachment{}, false, ErrUsageAttachment
	}
	return out, true, nil
}
