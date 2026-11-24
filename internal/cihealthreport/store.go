package cihealthreport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

const (
	publicationStoreFormat     = "garm-test-report-publications-v1"
	publicationStoreMaxBytes   = 8 << 20
	publicationStoreMaxRecords = 4096
	publicationStoreMaxScopes  = 64
)

// PublicationFileStore is a single-writer local snapshot store for retained
// test report publications. It is the offline reference implementation of
// Store: atomic rename publication, fail-closed open, idempotent saves,
// cross-attempt reuse rejection and retention purges by the shared history
// window. Long-lived multi-writer durability belongs to the Plan 05 history
// lifecycle; this store never invents a second authority.
type PublicationFileStore struct {
	mu     sync.Mutex
	path   string
	now    func() time.Time
	state  publicationStoreState
	closed bool
}

type publicationStoreState struct {
	Format       string              `json:"format"`
	Publications []StoredPublication `json:"publications"`
}

// OpenPublicationStore opens (or initializes) one private local snapshot
// file. An existing unreadable or foreign file is refused, never truncated
// or rewritten: a damaged store is an operator decision, not silent loss.
func OpenPublicationStore(path string) (*PublicationFileStore, error) {
	return openPublicationStore(path, time.Now)
}

func openPublicationStore(path string, now func() time.Time) (*PublicationFileStore, error) {
	if path == "" || now == nil {
		return nil, ErrReportStore
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrReportStore
	}
	s := &PublicationFileStore{path: absolute, now: now, state: publicationStoreState{Format: publicationStoreFormat, Publications: []StoredPublication{}}}
	info, err := os.Lstat(absolute)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := s.persist(); err != nil {
			return nil, err
		}
	case err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0:
		return nil, ErrReportStore
	default:
		raw, err := os.ReadFile(absolute)
		if err != nil || len(raw) > publicationStoreMaxBytes {
			return nil, ErrReportStore
		}
		var state publicationStoreState
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&state) != nil || d.Decode(new(any)) != io.EOF ||
			state.Format != publicationStoreFormat || !s.adopt(state) {
			return nil, ErrReportStore
		}
	}
	s.purge()
	return s, nil
}

func validRetentionScope(scope cihealth.HistoryScope) bool {
	return sourceAtom(scope.Source) && scope.RepositoryID > 0 && scope.WorkflowID > 0
}

func validAttemptKey(attempt cihealth.AttemptKey) bool {
	return sourceAtom(attempt.Source) && attempt.RepositoryID > 0 && attempt.WorkflowID > 0 && attempt.RunID > 0 && attempt.Attempt > 0
}

// adopt sanity-checks every retained record before it becomes readable.
func (s *PublicationFileStore) adopt(state publicationStoreState) bool {
	seen := make(map[cihealth.AttemptKey]bool, len(state.Publications))
	for _, pub := range state.Publications {
		if !validStoredPublication(pub) || seen[pub.Attempt] {
			return false
		}
		seen[pub.Attempt] = true
	}
	s.state = state
	if s.state.Publications == nil {
		s.state.Publications = []StoredPublication{}
	}
	return true
}

func validStoredPublication(pub StoredPublication) bool {
	if !validRetentionScope(pub.Scope) || pub.Scope != (cihealth.HistoryScope{Source: pub.Attempt.Source, RepositoryID: pub.Attempt.RepositoryID, WorkflowID: pub.Attempt.WorkflowID}) ||
		!validAttemptKey(pub.Attempt) || pub.ReceivedAt.IsZero() {
		return false
	}
	if len(pub.Reports)+len(pub.Metadata) == 0 || len(pub.Reports)+len(pub.Metadata) > cihealth.MaxRetainedAttemptReports {
		return false
	}
	artifacts := make(map[int64]bool, len(pub.Reports)+len(pub.Metadata))
	for _, report := range pub.Reports {
		if report.ArtifactID <= 0 || report.JobID <= 0 || report.Shard != "" && !sourceAtom(report.Shard) ||
			report.ArchiveSHA256 == ([sha256.Size]byte{}) || report.EntrySHA256 == ([sha256.Size]byte{}) || artifacts[report.ArtifactID] {
			return false
		}
		artifacts[report.ArtifactID] = true
	}
	for _, metadata := range pub.Metadata {
		if metadata.ArtifactID <= 0 || metadata.ArchiveSHA256 == ([sha256.Size]byte{}) || artifacts[metadata.ArtifactID] {
			return false
		}
		artifacts[metadata.ArtifactID] = true
	}
	return true
}

// SavePublication retains one publication idempotently. Re-saving the
// identical publication returns the existing record; a conflicting claim for
// the same attempt, or evidence already retained for a different attempt,
// refuses the save. Retention is purged by the shared history window.
func (s *PublicationFileStore) SavePublication(ctx context.Context, pub Publication) (SaveResult, error) {
	fail := func() (SaveResult, error) { return SaveResult{}, ErrReportStore }
	if ctx == nil || ctx.Err() != nil {
		return fail()
	}
	stored := StoredPublication{
		Scope: pub.Scope, Attempt: pub.Attempt, ReceivedAt: s.now().UTC(),
		Reports: slices.Clone(pub.Reports), Metadata: slices.Clone(pub.Metadata),
	}
	if !validStoredPublication(stored) {
		return fail()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fail()
	}
	s.purge()
	for _, prior := range s.state.Publications {
		if prior.Attempt != stored.Attempt {
			continue
		}
		if prior.Scope != stored.Scope || !reflect.DeepEqual(prior.Reports, stored.Reports) || !reflect.DeepEqual(prior.Metadata, stored.Metadata) {
			return fail() // A changed claim for one exact attempt is a conflict.
		}
		existing := prior
		existing.Reports = slices.Clone(prior.Reports)
		existing.Metadata = slices.Clone(prior.Metadata)
		return SaveResult{Record: existing, Created: false}, nil
	}
	if s.reusedAcrossAttempts(stored) || len(s.state.Publications) >= publicationStoreMaxRecords || s.wouldExceedScopes(stored.Scope) {
		return fail()
	}
	next := publicationStoreState{Format: publicationStoreFormat, Publications: append(slices.Clone(s.state.Publications), stored)}
	if err := s.persistState(next); err != nil {
		return fail()
	}
	s.state = next
	return SaveResult{Record: stored, Created: true}, nil
}

func (s *PublicationFileStore) reusedAcrossAttempts(stored StoredPublication) bool {
	for _, prior := range s.state.Publications {
		if prior.Attempt == stored.Attempt || prior.Attempt.Source != stored.Attempt.Source || prior.Attempt.RepositoryID != stored.Attempt.RepositoryID {
			continue
		}
		claimed := make(map[int64]bool, len(prior.Reports)+len(prior.Metadata))
		for _, report := range prior.Reports {
			claimed[report.ArtifactID] = true
		}
		for _, metadata := range prior.Metadata {
			claimed[metadata.ArtifactID] = true
		}
		for _, report := range stored.Reports {
			if claimed[report.ArtifactID] {
				return true
			}
		}
		for _, metadata := range stored.Metadata {
			if claimed[metadata.ArtifactID] {
				return true
			}
		}
	}
	return false
}

func (s *PublicationFileStore) wouldExceedScopes(scope cihealth.HistoryScope) bool {
	seen := map[cihealth.HistoryScope]bool{scope: true}
	for _, pub := range s.state.Publications {
		seen[pub.Scope] = true
	}
	return len(seen) > publicationStoreMaxScopes
}

// LoadPublication returns the retained record for one exact attempt.
func (s *PublicationFileStore) LoadPublication(_ context.Context, attempt cihealth.AttemptKey) (StoredPublication, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !validAttemptKey(attempt) {
		return StoredPublication{}, false, ErrReportStore
	}
	for _, pub := range s.state.Publications {
		if pub.Attempt == attempt {
			loaded := pub
			loaded.Reports = slices.Clone(pub.Reports)
			loaded.Metadata = slices.Clone(pub.Metadata)
			return loaded, true, nil
		}
	}
	return StoredPublication{}, false, nil
}

// Close releases the store. It is idempotent and never writes on close.
func (s *PublicationFileStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// purge drops publications past the shared retention window. Retention is
// scoped: purging one repository never touches another's records.
func (s *PublicationFileStore) purge() {
	cutoff := s.now().UTC().Add(-cihealth.HistoryRetentionWindow)
	kept := s.state.Publications[:0]
	for _, pub := range s.state.Publications {
		if !pub.ReceivedAt.Before(cutoff) {
			kept = append(kept, pub)
		}
	}
	for i := len(kept); i < len(s.state.Publications); i++ {
		s.state.Publications[i] = StoredPublication{}
	}
	s.state.Publications = kept
	if s.state.Publications == nil {
		s.state.Publications = []StoredPublication{}
	}
}

func (s *PublicationFileStore) persist() error {
	return s.persistState(s.state)
}

func (s *PublicationFileStore) persistState(state publicationStoreState) error {
	raw, err := json.Marshal(state)
	if err != nil || len(raw) > publicationStoreMaxBytes {
		return ErrReportStore
	}
	dir := filepath.Dir(s.path)
	temp, err := os.CreateTemp(dir, ".test-report-publications-*.stage")
	if err != nil {
		return ErrReportStore
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return ErrReportStore
	}
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return ErrReportStore
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return ErrReportStore
	}
	if err := temp.Close(); err != nil {
		return ErrReportStore
	}
	if err := os.Rename(temp.Name(), s.path); err != nil {
		return ErrReportStore
	}
	d, err := os.Open(dir)
	if err != nil {
		return ErrReportStore
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return ErrReportStore
	}
	return nil
}
