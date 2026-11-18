//go:build linux

// Package jobobserver performs positive authenticated lifecycle reads and durable
// correlation. No absence, network receipt or job result authorizes deletion.
package jobobserver

import (
	"context"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"sync"
	"time"
)

var ErrConfiguration = errors.New("job observer unavailable; retain unresolved lineage and charge")

type Owner struct {
	Connection nativeidentity.Configuration
	Policy     nativeapi.Policy
}
type Configuration struct {
	Store              *admission.SQLiteStore
	GitHub             *demandrepair.GitHubClient
	Repositories       []demandrepair.Repository
	Owners             []Owner
	Lookback, Interval time.Duration
}
type Service struct {
	statusMu           sync.RWMutex
	status             Status
	mu                 sync.Mutex
	store              *admission.SQLiteStore
	github             *demandrepair.GitHubClient
	repos              map[string]demandrepair.Repository
	owners             map[string]Owner
	lookback, interval time.Duration
	after              string
}
type Status struct {
	At         time.Time
	Result     Result
	Incomplete bool
}

func (s *Service) Latest() Status {
	if s == nil {
		return Status{Incomplete: true}
	}
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return s.status
}

type Result struct{ Examined, JobsRecorded, RootFencesRecorded, Unresolved int }

func New(c Configuration) (*Service, error) {
	if c.Store == nil || c.GitHub == nil || len(c.Repositories) == 0 || len(c.Owners) == 0 || c.Lookback < time.Minute || c.Lookback > 31*24*time.Hour || c.Interval < time.Second {
		return nil, ErrConfiguration
	}
	s := &Service{store: c.Store, github: c.GitHub, repos: map[string]demandrepair.Repository{}, owners: map[string]Owner{}, lookback: c.Lookback, interval: c.Interval}
	for _, r := range c.Repositories {
		if r.GarmID == "" || r.Host == "" || r.Owner == "" || r.Name == "" {
			return nil, ErrConfiguration
		}
		if _, ok := s.repos[r.GarmID]; ok {
			return nil, ErrConfiguration
		}
		s.repos[r.GarmID] = r
	}
	for _, o := range c.Owners {
		if !o.Policy.Protected() || o.Connection.DomainID != o.Policy.DomainID() || o.Connection.PolicyDigest != o.Policy.Digest() {
			return nil, ErrConfiguration
		}
		if _, ok := s.owners[o.Connection.DomainID]; ok {
			return nil, ErrConfiguration
		}
		var e error
		o.Connection.TLS, e = freezeTLS(o.Connection.TLS)
		if e != nil {
			return nil, e
		}
		s.owners[o.Connection.DomainID] = o
	}
	return s, nil
}

// Run retries durable unknown records. Cancellation never changes accounting.
// The in-memory page cursor is fairness only, never a completion checkpoint.
func (s *Service) Run(ctx context.Context) error {
	if s == nil || ctx == nil {
		return ErrConfiguration
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			_, _ = s.Cycle(ctx)
			timer.Reset(s.interval)
		}
	}
}
func (s *Service) Cycle(ctx context.Context) (result Result, err error) {
	if s == nil || ctx == nil || ctx.Err() != nil {
		return Result{}, ErrConfiguration
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		s.statusMu.Lock()
		s.status = Status{At: time.Now().UTC(), Result: result, Incomplete: err != nil || result.Unresolved > 0}
		s.statusMu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	bindings, e := s.store.NativeRunnerBindings(ctx, s.after, 64)
	if e != nil {
		return Result{}, e
	}
	if len(bindings) == 0 {
		s.after = ""
		return Result{}, nil
	}
	out := Result{}
	type discovery struct {
		jobs []demandrepair.Job
		err  error
	}
	found := map[string]discovery{}
	for _, b := range bindings {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		out.Examined++
		s.after = b.PermitID
		repo, ok := s.repos[b.Instance.Repository]
		owner, owned := s.owners[b.DomainID]
		if !ok || !owned || b.Runner.GitHubURL != "https://"+repo.Host+"/"+repo.Owner+"/"+repo.Name || owner.Policy.Digest() != b.PolicyDigest {
			out.Unresolved++
			continue
		}
		// A historical root receipt is not reread as live fencing authority.
		if _, err := s.store.LookupNativeRootFence(ctx, b.PermitID); err == nil {
			continue
		}
		history, err := s.store.NativeJobHistory(ctx, b.PermitID)
		if err != nil {
			out.Unresolved++
			continue
		}
		completed := false
		var candidates []int64
		for _, j := range history {
			if j.Observation.Status == "completed" {
				completed = true
			}
			candidates = append(candidates, j.Observation.JobID)
		}
		if !completed && len(candidates) == 0 {
			d, exists := found[repo.GarmID]
			if !exists {
				now := time.Now().UTC()
				d.jobs, d.err = s.github.Read(ctx, repo, now.Add(-s.lookback), now)
				found[repo.GarmID] = d
			}
			if d.err != nil {
				out.Unresolved++
				continue
			}
			for _, j := range d.jobs {
				if j.Status == "in_progress" || j.Status == "completed" {
					candidates = append(candidates, j.ID)
				}
			}
		}
		if !completed {
			for _, id := range candidates {
				observation, err := s.github.ObserveExactJob(ctx, repo, id)
				if err != nil {
					continue
				}
				r := observation.Record()
				if r.RunnerID != b.Runner.RunnerID || r.RunnerName != b.Runner.Name {
					continue
				}
				if err = s.store.RecordNativeJob(ctx, b.PermitID, observation); err != nil {
					continue
				}
				out.JobsRecorded++
				completed = r.Status == "completed"
				break
			}
		}
		if !completed {
			out.Unresolved++
			continue
		}
		journal, err := nativeidentity.ObserveProtectedJournal(ctx, owner.Connection, b.PermitID)
		if err != nil {
			out.Unresolved++
			continue
		}
		if err = s.store.RecordNativeRootFence(ctx, journal, owner.Policy); err != nil {
			out.Unresolved++
			continue
		}
		out.RootFencesRecorded++
	}
	if len(bindings) < 64 {
		s.after = ""
	}
	return out, nil
}
