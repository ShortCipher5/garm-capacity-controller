package brokerinput

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsettings"
	"sort"
	"sync"
	"time"
)

var ErrPolicyCaps = errors.New("policy cap reconciliation unavailable; inspect managed journal")

// CapPoolResult is historical reporting only, never capacity or write authority.
type CapPoolResult struct {
	PoolID  string `json:"pool_id"`
	Outcome string `json:"outcome"`
}

type CapStatus struct {
	PolicyRevision int64           `json:"policy_revision"`
	State          string          `json:"state"`
	LastAttempt    time.Time       `json:"last_attempt,omitempty"`
	LastSuccess    time.Time       `json:"last_success,omitempty"`
	Confirmed      int             `json:"confirmed"`
	Skipped        int             `json:"skipped"`
	LastRequestID  string          `json:"last_request_id,omitempty"`
	PoolResults    []CapPoolResult `json:"pool_results,omitempty"`
}
type CapResult struct {
	PolicyRevision     int64
	Confirmed, Skipped int
	LastRequestID      string
	PoolResults        []CapPoolResult
}

// CapService only lowers positive pool maximum settings to saved policy caps.
// It never changes minimum idle, stops runners, releases charges or raises a
// maximum. It is separate from the still-gated grant/allocation loop.
type CapService struct {
	store    *admission.SQLiteStore
	client   *managedsettings.Client
	interval time.Duration
	mu       sync.Mutex
	running  bool
	status   CapStatus
}

func NewCapService(store *admission.SQLiteStore, client *managedsettings.Client, interval time.Duration) (*CapService, error) {
	if store == nil || !store.RuntimeLineageBound() || client == nil || interval < time.Minute || interval > time.Hour {
		return nil, ErrPolicyCaps
	}
	return &CapService{store: store, client: client, interval: interval, status: CapStatus{State: "not_started"}}, nil
}
func (s *CapService) Snapshot() CapStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.status
	out.PoolResults = append([]CapPoolResult(nil), s.status.PoolResults...)
	return out
}
func (s *CapService) Run(ctx context.Context) error {
	if ctx == nil {
		return ErrPolicyCaps
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return ErrPolicyCaps
	}
	s.running = true
	s.status.State = "running"
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		if s.status.State == "running" {
			s.status.State = "stopped"
		}
		s.mu.Unlock()
	}()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.mu.Lock()
		s.status.LastAttempt = time.Now().UTC()
		s.mu.Unlock()
		cycle, cancel := context.WithTimeout(ctx, 2*time.Minute)
		r, e := ReconcilePolicyCaps(cycle, s.store, s.client)
		cancel()
		s.mu.Lock()
		s.status.PolicyRevision = r.PolicyRevision
		s.status.Confirmed = r.Confirmed
		s.status.Skipped = r.Skipped
		s.status.LastRequestID = r.LastRequestID
		s.status.PoolResults = append([]CapPoolResult(nil), r.PoolResults...)
		if e != nil {
			s.status.State = "blocked"
			s.mu.Unlock()
			return e
		}
		s.status.LastSuccess = time.Now().UTC()
		s.mu.Unlock()
		timer := time.NewTimer(s.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// ReconcilePolicyCaps stops on the first unconfirmed operation. A restart may
// read again but cannot replay writes: the supervisor's sole durable journal
// blocks uncertainty, and confirmed lower settings are skipped on the next pass.
func ReconcilePolicyCaps(ctx context.Context, store *admission.SQLiteStore, client *managedsettings.Client) (CapResult, error) {
	var out CapResult
	if ctx == nil || store == nil || client == nil {
		return out, ErrPolicyCaps
	}
	initial, e := store.ReadBrokerGrantState(ctx)
	if e != nil || !initial.Ready {
		return out, ErrPolicyCaps
	}
	out.PolicyRevision = initial.Policy.Revision
	snapshot, e := client.Snapshot(ctx)
	if e != nil || !snapshot.WritesEnabled || snapshot.Status.WriteBlocked {
		return out, ErrPolicyCaps
	}
	source, controller, e := client.ObservedSource(ctx)
	if e != nil {
		return out, ErrPolicyCaps
	}
	targets, e := policyCapTargets(initial, snapshot.Pools, source, controller)
	if e != nil {
		return out, e
	}
	for _, target := range targets {
		out.PoolResults = append(out.PoolResults, CapPoolResult{PoolID: target.pool, Outcome: "not_examined"})
	}
	for index, target := range targets {
		if out.Confirmed >= 10 {
			break
		}
		current, e := store.ReadBrokerGrantState(ctx)
		if e != nil || !current.Ready || current.Sequence != initial.Sequence || current.Policy.Revision != initial.Policy.Revision {
			return out, ErrPolicyCaps
		}
		if target.maximum == 0 {
			out.Skipped++
			out.PoolResults[index].Outcome = target.skipReason
			continue
		}
		id, e := capRequestID()
		if e != nil {
			return out, ErrPolicyCaps
		}
		out.LastRequestID = id
		_, e = client.ReduceMaximum(ctx, id, target.pool, target.fingerprint, target.maximum, fmt.Sprintf("Reconcile saved policy revision %d pool cap; no runner retirement", initial.Policy.Revision))
		if e != nil {
			out.PoolResults[index].Outcome = "unconfirmed"
			return out, ErrPolicyCaps
		}
		out.Confirmed++
		out.PoolResults[index].Outcome = "confirmed_reduction"
	}
	return out, nil
}

type capTarget struct {
	pool, fingerprint string
	maximum           uint64
	skipReason        string
}

func policyCapTargets(state admission.BrokerGrantState, pools []garmapi.ManagedPool, source, controller string) ([]capTarget, error) {
	if source == "" || controller == "" || len(pools) > 1000 {
		return nil, ErrPolicyCaps
	}
	caps := map[string]admission.BrokerPoolPolicy{}
	repos := map[string]string{}
	for _, r := range state.Policy.Repositories {
		for _, p := range r.Pools {
			caps[p.ID] = p
			repos[p.ID] = r.ID
		}
	}
	out := []capTarget{}
	seen := map[string]bool{}
	for _, p := range pools {
		cap, ok := caps[p.ID]
		if !ok {
			continue
		}
		profile, ok := state.PoolProfiles[p.ID]
		if !ok || seen[p.ID] || profile.SourceIdentity != source || profile.Authority.Controller != controller || profile.Authority.PoolID != p.ID || profile.Authority.ProviderName != p.ProviderName || profile.Pool != p.ID || profile.Repository != repos[p.ID] || p.RepositoryID != profile.Repository {
			return nil, ErrPolicyCaps
		}
		seen[p.ID] = true
		t := capTarget{pool: p.ID, fingerprint: p.Fingerprint, skipReason: capSkipReason(cap.MaxRunners, p.MaxRunners, p.MinIdleRunners)}
		if cap.MaxRunners > 0 && cap.MaxRunners <= 1000000 && uint64(cap.MaxRunners) < p.MaxRunners && uint64(cap.MaxRunners) >= p.MinIdleRunners {
			t.maximum = uint64(cap.MaxRunners)
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pool < out[j].pool })
	return out, nil
}
func capRequestID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}

func capSkipReason(cap int64, current, idle uint64) string {
	if cap == 0 {
		return "zero_cap_unsupported"
	}
	if cap < 0 || cap > 1000000 {
		return "unsupported_cap"
	}
	if uint64(cap) >= current {
		return "no_downward_change"
	}
	if uint64(cap) < idle {
		return "below_idle_minimum"
	}
	return "not_examined"
}
