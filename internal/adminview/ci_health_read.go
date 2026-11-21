package adminview

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

const (
	ciDisplayLifetime  = 2 * time.Minute
	maxCIResponseBytes = 1 << 20
)

// CIHealthRepository is an exact identity from already-authorized startup
// configuration. Authenticated dashboard readers have the existing owner-wide
// read role, not individual per-repository permissions.
type CIHealthRepository struct {
	Source       string
	RepositoryID int64
}

// CIHealthSnapshot is an optional view publisher for background collectors.
// Request handling reads a bounded pre-encoded snapshot from memory only.
// A zero-value instance is not a configured source.
type CIHealthSnapshot struct {
	scopes     []ciViewScope
	clock      func() time.Time
	mu         sync.Mutex
	latest     time.Time
	checked    time.Time
	generation *ciHealthGeneration
	body       []byte
	history    ciHistoryResponse
	workflows  map[cihealth.TrendScope]bool
}

type ciHealthGeneration struct{ reserved byte }

func NewCIHealthSnapshot(approved []CIHealthRepository) (*CIHealthSnapshot, error) {
	if len(approved) == 0 || len(approved) > maxCIScopes {
		return nil, ErrConfiguration
	}
	scopes := make([]ciViewScope, 0, len(approved))
	seen := make(map[ciViewScope]bool, len(approved))
	for _, repo := range approved {
		scope := ciViewScope{repo.Source, repo.RepositoryID}
		if !ciHealthSourceLabel(scope.source) || scope.repository <= 0 || seen[scope] {
			return nil, ErrConfiguration
		}
		scopes = append(scopes, scope)
		seen[scope] = true
	}
	return &CIHealthSnapshot{scopes: scopes, clock: time.Now, generation: &ciHealthGeneration{}}, nil
}

func ciHealthSourceLabel(label string) bool {
	if len(label) == 0 || len(label) > 64 {
		return false
	}
	for _, c := range label {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// Publish replaces the visible subset supplied by a trusted publisher. It
// does not imply exhaustive GitHub history. On a failed collection the caller
// must Withdraw. A later publication cannot resurrect data at or before that
// withdrawal. The publisher must own the supplied facts until this returns.
func (s *CIHealthSnapshot) Publish(observedAt time.Time, facts []cihealth.AttemptFacts) error {
	return s.PublishTiming(observedAt, facts, false)
}

// PublishTiming distinguishes local bounds from an ordinary observed subset.
// Neither mode establishes complete or atomic GitHub history.
func (s *CIHealthSnapshot) PublishTiming(observedAt time.Time, facts []cihealth.AttemptFacts, boundedPartial bool) error {
	if s == nil || len(s.scopes) == 0 {
		return ErrConfiguration
	}
	at := observedAt.UTC()
	lease := s.begin(at)
	if lease == nil {
		return errCIProjection
	}
	view, err := projectCIHealthTiming(s.scopes, facts)
	if err != nil {
		return err
	}
	for _, f := range facts {
		if f.AsOf.UTC().After(at) {
			return errCIProjection
		}
	}
	var workflows map[cihealth.TrendScope]bool
	if !boundedPartial {
		workflows = make(map[cihealth.TrendScope]bool, len(facts))
		for _, f := range facts {
			k := f.Key
			workflows[cihealth.TrendScope{Source: k.Source, RepositoryID: k.RepositoryID, WorkflowID: k.WorkflowID}] = true
		}
	}
	coverage := "observed_subset"
	if boundedPartial {
		coverage = "bounded_partial"
	}
	wire, err := json.Marshal(struct {
		State    string       `json:"state"`
		At       time.Time    `json:"observed_at"`
		Coverage string       `json:"coverage"`
		View     ciTimingView `json:"timing"`
	}{State: "observed", At: at, Coverage: coverage, View: view})
	if err != nil || len(wire) > maxCIResponseBytes {
		return errCIProjection
	}
	return s.commit(at, lease, wire, workflows)
}

func (s *CIHealthSnapshot) begin(at time.Time) *ciHealthGeneration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.validAtLocked(at) {
		return nil
	}
	return s.generation
}

func (s *CIHealthSnapshot) commit(at time.Time, lease *ciHealthGeneration, wire []byte, workflows map[cihealth.TrendScope]bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.validAtLocked(at) || lease != s.generation {
		return errCIProjection
	}
	s.latest, s.body, s.generation = at, wire, &ciHealthGeneration{}
	s.history = ciHistoryResponse{}
	s.workflows = workflows
	return nil
}

func (s *CIHealthSnapshot) validAtLocked(at time.Time) bool {
	now, stable := s.wallLocked()
	return stable && !at.IsZero() && at.After(s.latest) && !at.After(now) && now.Before(at.Add(ciDisplayLifetime))
}

// wallLocked samples the clock after acquiring the lock. An observed wall
// reversal clears the result and fences pending publication, preserving the
// largest time we have actually observed until the clock catches up.
func (s *CIHealthSnapshot) wallLocked() (time.Time, bool) {
	now := s.clock().UTC()
	if now.Before(s.checked) {
		s.fenceLocked()
		return now, false
	}
	s.checked = now
	return now, true
}

// Withdraw clears the current result and fences every in-flight publication.
func (s *CIHealthSnapshot) Withdraw() {
	if s == nil || s.clock == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.wallLocked()
	s.fenceLocked()
}

func (s *CIHealthSnapshot) fenceLocked() {
	s.body, s.generation = nil, &ciHealthGeneration{}
	s.history = ciHistoryResponse{}
	s.workflows = nil
	if s.checked.After(s.latest) {
		s.latest = s.checked
	}
}

func (s *CIHealthSnapshot) read() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	now, stable := s.wallLocked()
	if !stable || now.Before(s.latest) || !now.Before(s.latest.Add(ciDisplayLifetime)) {
		s.body = nil
		s.history = ciHistoryResponse{}
		s.workflows = nil
	}
	if s.history.body != nil {
		if now.Before(s.history.expires) && s.history.source.Current() {
			return s.history.body
		}
		s.history = ciHistoryResponse{}
	}
	return s.body
}

// SetCIHealthSnapshot is a one-time explicit startup attachment. This adds no
// route without a configured source and does not initiate I/O or poll GitHub.
func (h *Handler) SetCIHealthSnapshot(s *CIHealthSnapshot) error {
	if s == nil || len(s.scopes) == 0 || !h.ciHealth.CompareAndSwap(nil, s) {
		return ErrConfiguration
	}
	return nil
}

func (h *Handler) serveCIHealth(w http.ResponseWriter, r *http.Request, source *CIHealthSnapshot) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !adminAuthenticated(r, h.tokenHash) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body := source.read()
	if body == nil {
		_, _ = w.Write([]byte("{\"state\":\"unavailable\",\"timing\":{\"attempts\":[]}}\n"))
		return
	}
	_, _ = w.Write(body)
}
