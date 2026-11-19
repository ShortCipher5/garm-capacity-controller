// Package scalingadvice keeps bounded offline proposal history. It performs no
// I/O, dispatch, persistence, allocation or settings changes. Its values are not
// write preconditions or evidence that a proposal was applied.
package scalingadvice

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxRecords    = 64
	MaxStateBytes = 64 << 10
	KnownAdvice   = "known_advice"
	Uncertain     = "uncertain"
)

// MaxObservationLifetime matches newAdvisoryReader's time.Minute MaxAge in
// internal/brokerinput/advisory_linux.go. projectReadPlan starts this deadline
// at StartedAt; host evidence, prior comparison and pressure only shorten it.
// This cap is not a claim of freshness or authenticated acquisition.
const MaxObservationLifetime = time.Minute

var ErrState = errors.New("invalid or conflicting advisory history")

// Record describes advice, never an acknowledged service mutation. Certainty
// refers only to the remembered proposal; known_advice does not mean applied.
type Record struct {
	ID           string    `json:"id"`
	Source       string    `json:"source"`
	PoolID       string    `json:"pool_id"`
	RepositoryID string    `json:"repository_id"`
	Fingerprint  string    `json:"fingerprint"`
	Scope        [32]byte  `json:"scope"`
	Sequence     int64     `json:"sequence"`
	Revision     int64     `json:"revision"`
	Observed     int64     `json:"observed"`
	Suggested    int64     `json:"suggested"`
	ObservedAt   time.Time `json:"observed_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	Certainty    string    `json:"certainty"`
}

// State is copy-on-change. There is deliberately no reset, remove, replay or
// uncertainty-resolution operation. History exhaustion requires outside review.
type State struct {
	records  []Record
	restored bool
}
type wireState struct {
	Version string   `json:"version"`
	Records []Record `json:"records"`
}

func New() State                  { return State{records: []Record{}} }
func (s State) Records() []Record { return append([]Record{}, s.records...) }
func text(s string) bool {
	return len(s) > 0 && len(s) <= 256 && utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool { return r < 33 || r == 127 }) < 0
}
func valid(r Record) bool {
	return r.Scope != ([32]byte{}) && text(r.ID) && text(r.Source) && text(r.PoolID) && text(r.RepositoryID) && text(r.Fingerprint) && r.Sequence > 0 && r.Revision >= 0 && r.Observed > 0 && r.Observed <= 1000000 && r.Suggested >= 1 && r.Suggested <= 1000000 && (r.Suggested-r.Observed == 1 || r.Suggested-r.Observed == -1) && !r.ObservedAt.IsZero() && r.ExpiresAt.After(r.ObservedAt) && r.ExpiresAt.Sub(r.ObservedAt) <= MaxObservationLifetime && (r.Certainty == KnownAdvice || r.Certainty == Uncertain)
}
func validRecords(rs []Record) bool {
	if rs == nil || len(rs) > MaxRecords {
		return false
	}
	ids, pools := map[string]bool{}, map[string]bool{}
	for _, r := range rs {
		if !valid(r) || ids[r.ID] || pools[r.PoolID] {
			return false
		}
		ids[r.ID] = true
		pools[r.PoolID] = true
	}
	return true
}

// Remember preserves one outstanding advisory record per pool. A caller cannot
// overwrite an uncertain or conflicting earlier proposal with a newer one.
func (s State) Remember(r Record) (State, error) {
	if s.restored || !validRecords(s.records) || !valid(r) || len(s.records) == MaxRecords {
		return s, ErrState
	}
	for _, old := range s.records {
		if old.ID == r.ID || old.PoolID == r.PoolID {
			return s, ErrState
		}
	}
	out := State{records: s.Records()}
	out.records = append(out.records, r)
	if _, err := out.Encode(); err != nil {
		return s, err
	}
	return out, nil
}
func (s State) MarkUncertain(id string) (State, error) {
	if !validRecords(s.records) {
		return s, ErrState
	}
	out := State{records: s.Records(), restored: s.restored}
	for i := range out.records {
		if out.records[i].ID == id {
			out.records[i].Certainty = Uncertain
			return out, nil
		}
	}
	return s, ErrState
}

// Encode returns bytes only. No file is opened or journal written.
func (s State) Encode() ([]byte, error) {
	if !validRecords(s.records) {
		return nil, ErrState
	}
	b, e := json.Marshal(wireState{"scaling-advice/v1", s.records})
	if e != nil || len(b) > MaxStateBytes {
		return nil, ErrState
	}
	return b, nil
}

// Decode accepts only this version's canonical Encode representation. Exact
// round-trip equality rejects duplicate/unknown/case-aliased keys, null arrays,
// alternate field order/types and trailing data. This is NOT a trusted storage
// signature. Every decoded state is historical and blocks planning, regardless
// of wall clock or a coincidentally matching current maximum.
func Decode(raw []byte) (State, error) {
	if len(raw) == 0 || len(raw) > MaxStateBytes {
		return State{}, ErrState
	}
	var w wireState
	if json.Unmarshal(raw, &w) != nil || w.Version != "scaling-advice/v1" || !validRecords(w.Records) {
		return State{}, ErrState
	}
	s := State{records: append([]Record{}, w.Records...), restored: true}
	canonical, e := s.Encode()
	if e != nil || !bytes.Equal(raw, canonical) {
		return State{}, ErrState
	}
	return s, nil
}

// Observation is supplied comparison data, not a claim of authenticated or
// atomic acquisition. Its validation cannot establish accepted starts/cleanup.
type Observation struct {
	Source, PoolID, RepositoryID, Fingerprint string
	Scope                                     [32]byte
	Sequence, Revision, Maximum               int64
	ObservedAt, ExpiresAt                     time.Time
}
type Assessment struct {
	PlanningBlocked bool
	ReplayAllowed   bool // Always false, including for unchanged known advice.
	Reason          string
}

// Assess only vetoes or describes a comparison. A non-blocked result does not
// authorize a write and cannot replace the separate change-plan validator.
func (s State) Assess(id string, o Observation, now time.Time) Assessment {
	deny := func(reason string) Assessment { return Assessment{PlanningBlocked: true, Reason: reason} }
	if !validRecords(s.records) || now.IsZero() {
		return deny("invalid_history")
	}
	var r Record
	found := false
	for _, candidate := range s.records {
		if candidate.ID == id {
			r = candidate
			found = true
			break
		}
	}
	if !found {
		return deny("proposal_not_found")
	}
	if r.Certainty == Uncertain {
		return deny("uncertain_proposal_requires_review")
	}
	if s.restored {
		return deny("restored_history_requires_review")
	}
	if !r.ExpiresAt.After(now) {
		return deny("proposal_expired")
	}
	if o.ObservedAt.IsZero() || o.ObservedAt.After(now) || o.ObservedAt.Before(r.ObservedAt) || !o.ExpiresAt.After(now) || !o.ExpiresAt.After(o.ObservedAt) || o.ExpiresAt.Sub(o.ObservedAt) > MaxObservationLifetime {
		return deny("observation_missing_or_stale")
	}
	if o.Scope != r.Scope || o.Source != r.Source || o.PoolID != r.PoolID || o.RepositoryID != r.RepositoryID || o.Fingerprint != r.Fingerprint || o.Sequence != r.Sequence || o.Revision != r.Revision {
		return deny("observation_scope_conflict")
	}
	if o.Maximum != r.Observed {
		return deny("observed_maximum_conflict")
	}
	return Assessment{Reason: "unchanged_advice_only"}
}
