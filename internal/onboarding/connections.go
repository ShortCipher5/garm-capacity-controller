package onboarding

import (
	"context"
	"sort"
	"sync"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

// ConnectionTarget is deployment-owned authority for one credential alias.
// Factory constructs a fresh pinned transport without contacting its target.
// The browser cannot change this target, path, resolver or credential material.
type ConnectionTarget struct {
	Endpoint    string
	Project     string
	ExpectedKey string
	Factory     func(admission.Connection) (Observer, func(), error)
}
type ConnectionService struct {
	store   *admission.SQLiteStore
	targets map[string]ConnectionTarget
	static  map[string]bool
	mu      sync.Mutex
	active  map[string]map[*connectionCall]bool
}
type connectionCall struct{ cancel context.CancelFunc }

func NewConnectionService(store *admission.SQLiteStore, targets map[string]ConnectionTarget, staticIDs []string) (*ConnectionService, error) {
	if store == nil || len(targets) == 0 || len(targets) > 32 {
		return nil, ErrConfiguration
	}
	s := &ConnectionService{store: store, targets: map[string]ConnectionTarget{}, static: map[string]bool{}, active: map[string]map[*connectionCall]bool{}}
	for _, id := range staticIDs {
		s.static[id] = true
	}
	for id, t := range targets {
		if !admission.ValidOnboardingProfileID(id) || !admission.ValidConnectionEndpoint(t.Endpoint) || !admission.ValidConnectionProject(t.Project) || t.Factory == nil {
			return nil, ErrConfiguration
		}
		s.targets[id] = t
	}
	records, err := store.Connections(context.Background())
	if err != nil {
		return nil, err
	}
	for _, c := range records {
		if s.static[c.ID] {
			return nil, ErrConfiguration
		}
	}
	if err := store.InvalidateConnectionReviews(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *ConnectionService) allowed(c admission.Connection) bool {
	t, ok := s.targets[c.CredentialRef]
	return ok && !s.static[c.ID] && c.Endpoint == t.Endpoint && c.Project == t.Project && (t.ExpectedKey == "" || c.ServerKeySHA256 == t.ExpectedKey)
}
func (s *ConnectionService) List(ctx context.Context) ([]admission.Connection, error) {
	return s.store.Connections(ctx)
}
func (s *ConnectionService) Put(ctx context.Context, c admission.Connection, expected int64) (admission.Connection, error) {
	if !s.allowed(c) {
		return admission.Connection{}, ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	updated, err := s.store.PutConnection(ctx, c, expected)
	if err == nil {
		s.cancelLocked(c.ID)
	}
	return updated, err
}
func (s *ConnectionService) Transition(ctx context.Context, id string, expected int64, state, key string) (admission.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.store.Connection(ctx, id)
	if err != nil {
		return c, err
	}
	// Revocation remains possible after a deployment policy withdraws a target.
	if state != "revoked" && !s.allowed(c) {
		return c, ErrDenied
	}
	updated, err := s.store.TransitionConnection(ctx, id, expected, state, key)
	if err == nil {
		s.cancelLocked(id)
	}
	return updated, err
}
func (s *ConnectionService) cancelLocked(id string) {
	for call := range s.active[id] {
		call.cancel()
	}
}

// Resolve serializes a revision read with lifecycle transitions and registers
// cancellation before releasing the lock. Revocation cancels active read-only
// calls; its durable revision also rejects late results at review persistence.
func (s *ConnectionService) Resolve(ctx context.Context, id string) (context.Context, Observer, int64, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.store.Connection(ctx, id)
	if err != nil || c.State != "approved" || !s.allowed(c) {
		return ctx, nil, 0, func() {}, ErrDenied
	}
	observer, close, err := s.targets[c.CredentialRef].Factory(c)
	if err != nil || observer == nil {
		if close != nil {
			close()
		}
		return ctx, nil, 0, func() {}, ErrUnavailable
	}
	if close == nil {
		close = func() {}
	}
	child, cancel := context.WithCancel(ctx)
	call := &connectionCall{cancel: cancel}
	if s.active[id] == nil {
		s.active[id] = map[*connectionCall]bool{}
	}
	s.active[id][call] = true
	cleanup := func() {
		cancel()
		close()
		s.mu.Lock()
		delete(s.active[id], call)
		if len(s.active[id]) == 0 {
			delete(s.active, id)
		}
		s.mu.Unlock()
	}
	return child, observer, c.Revision, cleanup, nil
}

type ConnectionTargetView struct {
	Ref         string `json:"credential_ref"`
	Endpoint    string `json:"endpoint"`
	Project     string `json:"project"`
	ExpectedKey string `json:"expected_key,omitempty"`
}

func (s *ConnectionService) Targets() []ConnectionTargetView {
	targets := []ConnectionTargetView{}
	for ref, t := range s.targets {
		targets = append(targets, ConnectionTargetView{ref, t.Endpoint, t.Project, t.ExpectedKey})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Ref < targets[j].Ref })
	return targets
}
func (s *ConnectionService) ApprovedProfiles(ctx context.Context) ([]Profile, error) {
	records, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	profiles := []Profile{}
	for _, c := range records {
		if c.State == "approved" && s.allowed(c) {
			profiles = append(profiles, Profile{ID: c.ID})
		}
	}
	return profiles, nil
}
