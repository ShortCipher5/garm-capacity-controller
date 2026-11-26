package cihealthsource

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
)

// ObservedJobUsage belongs to one exact live cycle and one independently
// retained usage observation. Its output contains only the sanitized wire.
type ObservedJobUsage struct {
	caller context.Context
	key    cihealth.JobKey
	proof  WorkflowHistoryProof
	view   cihealth.JobUsageDisplay
}

func (h *CycleHistory) ObserveJobUsage(ctx context.Context, cycle Cycle, store *cihealth.UsageAttachmentStore, cursor cihealth.HistoryCursor, job cihealth.JobKey) (ObservedJobUsage, bool) {
	var none ObservedJobUsage
	k := job.Attempt
	if !h.allows(cihealth.HistoryScope{Source: k.Source, RepositoryID: k.RepositoryID, WorkflowID: k.WorkflowID}) || h.owner != cycle.source || store == nil {
		return none, false
	}
	proof, ok := cycle.HistoryProof(job.Attempt)
	if !ok {
		return none, false
	}
	row, ok := cycle.observedJobClock(job)
	if !ok {
		return none, false
	}
	view, ok := store.ObserveDisplay(ctx, h.history, cursor, job, row, cycle.ObservedAt())
	if !ok {
		return none, false
	}
	out := ObservedJobUsage{caller: ctx, key: job, proof: proof, view: view}
	if !out.Current() {
		return none, false
	}
	return out, true
}

func (u ObservedJobUsage) Current() bool {
	if u.caller == nil || u.caller.Err() != nil {
		return false
	}
	return u.proof.current(time.Now()) && u.view.Current()
}

func (u ObservedJobUsage) ForCycle(c Cycle, reader *demandobserver.CIHealthSource) (cihealth.JobKey, json.RawMessage, bool) {
	if reader == nil || c.source.observer != reader || u.proof.owner != c.source || u.proof.sealedAt != c.sealedAt || !u.Current() {
		return cihealth.JobKey{}, nil, false
	}
	wire := u.view.Wire()
	if len(wire) == 0 || !u.Current() {
		return cihealth.JobKey{}, nil, false
	}
	return u.key, wire, true
}
