package garmapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// DemandClient is a read-only, repository-scoped stock-GARM observation adapter.
// It has no settings writer, grant API or provider admission capability.
type DemandClient struct {
	discovery    *PoolDiscoveryClient
	repositories map[string]bool
}
type QueuedJob struct {
	WorkflowJobID int64     `json:"workflow_job_id"`
	RepositoryID  string    `json:"repository_id"`
	Labels        []string  `json:"labels"`
	CreatedAt     time.Time `json:"created_at"`
	// Locked means GARM has already attempted placement; it is not completion.
	Locked bool `json:"locked"`
	// Each job appears once even when several pools match. Do not count each
	// candidate as an independent job or infer an authoritative assignment.
	CandidatePoolIDs []string `json:"candidate_pool_ids"`
	// Disabled pools with matching labels and observed running idle supply.
	// Advisory-only: never grant, inflight or new-runner eligibility.
	IdleOnlyPoolIDs []string `json:"idle_only_pool_ids,omitempty"`
}
type RunnerSupply struct {
	PoolID   string `json:"pool_id"`
	Idle     int64  `json:"idle"`
	Creating int64  `json:"creating"`
	Busy     int64  `json:"busy"`
	Other    int64  `json:"other"`
}

// ObservedRunner is scoped stock identity evidence for planning deduplication.
// It is not native incarnation/completion evidence or a job assignment.
type ObservedRunner struct {
	RowID, Name, ProviderName, PoolID, Kind string
}
type DemandObservation struct {
	sourceIdentity string
	idleScope      *repositoryIdleScope
	Runners        []ObservedRunner `json:"runners"`
	StartedAt      time.Time        `json:"started_at"`
	FinishedAt     time.Time        `json:"finished_at"`
	Digest         string           `json:"digest"`
	Pools          []DiscoveredPool `json:"pools"`
	Jobs           []QueuedJob      `json:"jobs"`
	Supply         []RunnerSupply   `json:"supply"`
	// These reads are never atomic and do not cover webhooks GARM missed.
	Atomic                bool `json:"atomic"`
	MissedWebhookCoverage bool `json:"missed_webhook_coverage"`
}

func (o DemandObservation) SourceIdentity() string { return o.sourceIdentity }

func NewDemand(endpoint, token string, trust *tls.Config, providers, repositoryIDs []string) (*DemandClient, error) {
	if len(repositoryIDs) == 0 || len(repositoryIDs) > 1000 {
		return nil, ErrManagement
	}
	repos := map[string]bool{}
	for _, id := range repositoryIDs {
		if !managementID(id) || repos[id] {
			return nil, ErrManagement
		}
		repos[id] = true
	}
	discovery, err := NewPoolDiscovery(endpoint, token, trust, providers)
	if err != nil {
		return nil, err
	}
	return &DemandClient{discovery: discovery, repositories: repos}, nil
}
func (c *DemandClient) Close() {
	if c != nil && c.discovery != nil {
		c.discovery.Close()
	}
}
func (c *DemandClient) jobs(ctx context.Context, idleBlocked map[string]bool) ([]QueuedJob, error) {
	m := c.discovery.management
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.base+"/jobs", nil)
	if err != nil {
		return nil, ErrManagement
	}
	req.Header.Set("Authorization", "Bearer "+m.client.token)
	req.Header.Set("Accept", "application/json")
	res, err := m.client.http.Do(req)
	if err != nil {
		return nil, ErrManagement
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, ErrManagement
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 || !utf8.Valid(data) || ctx.Err() != nil {
		return nil, ErrManagement
	}
	var rows []json.RawMessage
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '[' || json.Unmarshal(data, &rows) != nil || len(rows) > 10000 {
		return nil, ErrManagement
	}
	out := []QueuedJob{}
	seen := map[string]bool{}
	for _, raw := range rows {
		f, e := row(raw)
		if e != nil || !preparationAliases(f, []string{"workflow_job_id", "repo_id", "status", "labels", "created_at", "locked_by", "scaleset_job_id"}) {
			return nil, ErrManagement
		}
		status, e := textField(f, "status", true)
		if e != nil {
			return nil, ErrManagement
		}
		if status != "queued" {
			if status != "in_progress" && status != "completed" {
				return nil, ErrManagement
			}
			continue
		}
		// Opt-in assessment sees only positively scoped queued rows. It must
		// not expose the existence of foreign or absent-repository jobs.
		if idleBlocked != nil {
			if repo, err := textField(f, "repo_id", false); err == nil && c.repositories[repo] {
				if scale, err := textField(f, "scaleset_job_id", false); err != nil || scale != "" {
					idleBlocked[repo] = true
				}
			}
		}
		scale, e := textField(f, "scaleset_job_id", false)
		if e != nil {
			return nil, ErrManagement
		}
		if scale != "" {
			continue
		}
		// Stock org/enterprise-only hooks may omit repo_id. Those jobs are
		// outside this explicitly repository-scoped view, not malformed.
		if raw, exists := f["repo_id"]; !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		repo, e := textField(f, "repo_id", true)
		if e != nil || !managementID(repo) {
			return nil, ErrManagement
		}
		if !c.repositories[repo] {
			continue
		}
		j := QueuedJob{RepositoryID: repo, CandidatePoolIDs: []string{}}
		if json.Unmarshal(f["workflow_job_id"], &j.WorkflowJobID) != nil || j.WorkflowJobID <= 0 {
			return nil, ErrManagement
		}
		key := repo + ":" + strconv.FormatInt(j.WorkflowJobID, 10)
		if seen[key] {
			return nil, ErrManagement
		}
		seen[key] = true
		if json.Unmarshal(f["labels"], &j.Labels) != nil || len(j.Labels) == 0 || len(j.Labels) > 100 {
			return nil, ErrManagement
		}
		labels := map[string]bool{}
		for i, l := range j.Labels {
			if !boundedSettingText(l, 256) || strings.TrimSpace(l) != l {
				return nil, ErrManagement
			}
			l = strings.ToLower(l)
			if labels[l] {
				return nil, ErrManagement
			}
			labels[l] = true
			j.Labels[i] = l
		}
		sort.Strings(j.Labels)
		if json.Unmarshal(f["created_at"], &j.CreatedAt) != nil || j.CreatedAt.IsZero() || j.CreatedAt.After(time.Now().UTC()) {
			return nil, ErrManagement
		}
		locked, e := textField(f, "locked_by", false)
		if e != nil {
			return nil, ErrManagement
		}
		if locked != "" && locked != "00000000-0000-0000-0000-000000000000" {
			if !managementID(locked) {
				return nil, ErrManagement
			}
			j.Locked = true
		}
		out = append(out, j)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		if out[i].WorkflowJobID != out[j].WorkflowJobID {
			return out[i].WorkflowJobID < out[j].WorkflowJobID
		}
		return out[i].RepositoryID < out[j].RepositoryID
	})
	return out, nil
}
func scopedPools(p PoolDiscovery, repos map[string]bool) []DiscoveredPool {
	out := []DiscoveredPool{}
	for _, pool := range p.Pools {
		if pool.OwnerKind == "repository" && repos[pool.OwnerID] {
			out = append(out, pool)
		}
	}
	return out
}

// Observe collects pools, jobs and runner supply with a second pool read to
// reject detected topology/settings drift. Matching reads are NOT a remote fence.
// It does not subtract supply from jobs: runners are not bound to a specific job,
// and doing that per matching pool would double-count/double-subtract shared demand.
func (c *DemandClient) Observe(ctx context.Context) (DemandObservation, error) {
	return c.observe(ctx, false)
}

func (c *DemandClient) observe(ctx context.Context, assessIdle bool) (DemandObservation, error) {
	if c == nil || c.discovery == nil || ctx == nil || ctx.Err() != nil {
		return DemandObservation{}, ErrManagement
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	start := time.Now().UTC()
	first, e := c.discovery.DiscoverPools(ctx)
	if e != nil {
		return DemandObservation{}, ErrManagement
	}
	var idleBlocked map[string]bool
	if assessIdle {
		idleBlocked = make(map[string]bool, len(c.repositories))
		for repo := range c.repositories {
			idleBlocked[repo] = false
		}
	}
	jobs, e := c.jobs(ctx, idleBlocked)
	if e != nil {
		return DemandObservation{}, ErrManagement
	}
	runners, e := c.discovery.management.client.Instances(ctx)
	if e != nil {
		return DemandObservation{}, ErrManagement
	}
	last, e := c.discovery.DiscoverPools(ctx)
	if e != nil || ctx.Err() != nil {
		return DemandObservation{}, ErrManagement
	}
	before := scopedPools(first, c.repositories)
	after := scopedPools(last, c.repositories)
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if !bytes.Equal(a, b) {
		return DemandObservation{}, ErrManagement
	}
	if len(after) > 1000 {
		return DemandObservation{}, ErrManagement
	}
	supplies := map[string]*RunnerSupply{}
	poolTags := map[string]map[string]bool{}
	for i, p := range after {
		// The normalized settings above can match when one stock read omitted
		// zero and the other explicitly returned it. Preserve eligibility,
		// but only report explicit evidence when both reads supplied it.
		if !before[i].HasExplicitMinimumIdle() {
			after[i].explicitMinIdle = nil
		}
		supplies[p.ID] = &RunnerSupply{PoolID: p.ID}
		poolTags[p.ID] = map[string]bool{}
		for _, tag := range p.Tags {
			poolTags[p.ID][strings.ToLower(tag)] = true
		}
	}
	for _, r := range runners {
		supply := supplies[r.PoolID]
		if supply == nil {
			continue
		}
		if r.Busy {
			supply.Busy++
		} else if r.Idle && r.State == "running" {
			supply.Idle++
		} else if r.SourcePreparationPending {
			supply.Creating++
		} else {
			supply.Other++
		}
	}
	observedRunners := []ObservedRunner{}
	for _, r := range runners {
		if supplies[r.PoolID] == nil {
			continue
		}
		kind := "other"
		if r.Busy {
			kind = "busy"
		} else if r.Idle && r.State == "running" {
			kind = "idle"
		} else if r.SourcePreparationPending {
			kind = "creating"
		}
		observedRunners = append(observedRunners, ObservedRunner{r.RowID, r.Name, r.ProviderName, r.PoolID, kind})
	}
	sort.Slice(observedRunners, func(i, j int) bool { return observedRunners[i].RowID < observedRunners[j].RowID })
	comparisons := 0
	for i := range jobs {
		for _, p := range after {
			comparisons++
			if comparisons > 1000000 || ctx.Err() != nil {
				return DemandObservation{}, ErrManagement
			}
			if p.OwnerID != jobs[i].RepositoryID {
				continue
			}
			tags := poolTags[p.ID]
			match := true
			for _, l := range jobs[i].Labels {
				if !tags[l] {
					match = false
					break
				}
			}
			if match {
				if p.Enabled {
					jobs[i].CandidatePoolIDs = append(jobs[i].CandidatePoolIDs, p.ID)
				} else if supplies[p.ID].Idle > 0 {
					jobs[i].IdleOnlyPoolIDs = append(jobs[i].IdleOnlyPoolIDs, p.ID)
				}
			}
		}
	}
	out := DemandObservation{sourceIdentity: c.discovery.management.ManagementIdentity(), Runners: observedRunners, StartedAt: start, FinishedAt: time.Now().UTC(), Pools: after, Jobs: jobs, Supply: []RunnerSupply{}}
	for _, p := range after {
		out.Supply = append(out.Supply, *supplies[p.ID])
	}
	canonical, _ := json.Marshal(struct {
		Pools   []DiscoveredPool
		Jobs    []QueuedJob
		Supply  []RunnerSupply
		Runners []ObservedRunner
	}{out.Pools, out.Jobs, out.Supply, out.Runners})
	if len(canonical) > 4<<20 || ctx.Err() != nil {
		return DemandObservation{}, ErrManagement
	}
	digest := sha256.Sum256(canonical)
	out.Digest = hex.EncodeToString(digest[:])
	if assessIdle {
		out.idleScope = assessRepositoryIdleScope(out, idleBlocked)
	}
	return out, nil
}
