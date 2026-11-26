package adminview

import (
	"encoding/json"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealthsource"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
)

// PublishLiveTimingWithUsage optionally enriches one background publication.
// Data received through HTTP cannot supply these exact retained observations.
// An unusable or oversized attachment leaves the ordinary timing publication.
func (s *CIHealthSnapshot) PublishLiveTimingWithUsage(cycle cihealthsource.Cycle, reader *demandobserver.CIHealthSource, usage []cihealthsource.ObservedJobUsage) error {
	if err := s.PublishLiveTiming(cycle, reader); err != nil {
		return err
	}
	if len(usage) == 0 || len(usage) > maxUsageJobs {
		return nil
	}
	checked := make(map[cihealth.JobKey]ciUsageJob, len(usage))
	for _, u := range usage {
		key, wire, ok := u.ForCycle(cycle, reader)
		if !ok {
			continue
		}
		if _, dup := checked[key]; dup {
			return nil
		}
		checked[key] = ciUsageJob{wire, u.ExpiresAt(), u.Current}
	}
	s.attachJobUsage(cycle.ObservedAt(), checked)
	return nil
}

const maxUsageJobs = 32

type ciUsageJob struct {
	wire    json.RawMessage
	expires time.Time
	current func() bool
}

func (s *CIHealthSnapshot) attachJobUsage(at time.Time, checked map[cihealth.JobKey]ciUsageJob) {
	s.mu.Lock()
	current := s.generation
	body := s.body
	ok := s.latest.Equal(at) && len(body) > 0 && len(checked) > 0 && len(checked) <= maxUsageJobs
	s.mu.Unlock()
	if !ok {
		return
	}
	var response ciTimingResponse
	if json.Unmarshal(body, &response) != nil {
		return
	}
	// Wire IDs are canonical decimal strings; preserve identities beyond 2^53.
	byRow := make(map[[6]string]ciUsageJob, len(checked))
	for key, entry := range checked {
		k := key.Attempt
		byRow[[6]string{k.Source, ciInt(k.RepositoryID), ciInt(k.WorkflowID), ciInt(k.RunID), ciInt(k.Attempt), ciInt(key.JobID)}] = entry
	}
	expires := at.Add(ciDisplayLifetime).UTC()
	var checks []func() bool
	for _, attempt := range response.View.Attempts {
		for i := range attempt.Jobs {
			row := &attempt.Jobs[i]
			entry, ok := byRow[[6]string{attempt.Source, attempt.RepoID, attempt.Workflow, attempt.Run, attempt.Attempt, row.ID}]
			if !ok || !entry.expires.After(at) || entry.current == nil || !entry.current() {
				continue
			}
			if entry.expires.Before(expires) {
				expires = entry.expires.UTC()
			}
			row.Usage = entry.wire
			checks = append(checks, entry.current)
		}
	}
	if len(checks) == 0 {
		return
	}
	ready := func() bool {
		for _, check := range checks {
			if !check() {
				return false
			}
		}
		return true
	}
	response.UsageExpiresAt = &expires
	combined, err := json.Marshal(response)
	if err != nil || len(combined) > maxCIResponseBytes {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current == s.generation && s.history.body == nil && s.clock().Before(expires) && ready() {
		s.usage = ciUsageResponse{body: combined, expires: expires, current: ready}
	}
}
