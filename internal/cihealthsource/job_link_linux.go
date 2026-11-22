package cihealthsource

import (
	"fmt"
	"strconv"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
)

// ObservedJobLink is a backend-only, local projection of one exact GitHub job
// in a bounded live cycle. It never stores a browser URL, accepts response URL
// fields, accesses GitHub, or grants viewer or upstream authorization.
type ObservedJobLink struct {
	run   ObservedWorkflowRunLink
	jobID int64
}

func (ObservedJobLink) Format(s fmt.State, _ rune) {
	_, _ = s.Write([]byte("[observed GitHub job link]"))
}

// URL constructs the destination only while the exact attempt observation and
// local reader remain current. Extracted addresses are not revocable proof
// and must not be stored, logged, or reused as live observations.
func (l ObservedJobLink) URL() (string, bool) {
	if l.jobID <= 0 {
		return "", false
	}
	href, current := l.run.URL()
	if !current {
		return "", false
	}
	return href + "/job/" + strconv.FormatInt(l.jobID, 10), true
}

// ObservedJobLink reuses the canonical GitHub.com run locator and its shorter
// attempt/source lease. The exact observed job ID, not its title or an attempt
// guess, identifies the rerun link. No matches can come from retained history
// or from a standalone source/attempt/job identity supplied by the caller.
func (c Cycle) ObservedJobLink(source *demandobserver.CIHealthSource, key cihealth.JobKey) (ObservedJobLink, bool) {
	var none ObservedJobLink
	if key.JobID <= 0 {
		return none, false
	}
	runLink, ok := c.ObservedWorkflowRunLink(source, key.Attempt)
	if !ok {
		return none, false
	}
	found := false
	for _, a := range c.facts {
		if a.Key.Source != key.Attempt.Source || a.Key.RepositoryID != key.Attempt.RepositoryID || a.Key.RunID != key.Attempt.RunID {
			continue
		}
		for _, job := range a.Jobs {
			if job.Key.JobID != key.JobID {
				continue
			}
			// The browser route cannot distinguish duplicate job IDs in the
			// same run. Conflicting evidence cannot mint either destination.
			if found || a.Key != key.Attempt || job.Key != key || job.Clock.ObservedAt.IsZero() ||
				job.Clock.ObservedAt.Before(a.Clock.ObservedAt) || job.Clock.ObservedAt.After(a.AsOf) {
				return none, false
			}
			found = true
		}
	}
	if !found {
		return none, false
	}
	return ObservedJobLink{run: runLink, jobID: key.JobID}, true
}
