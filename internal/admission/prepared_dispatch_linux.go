package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// GarmRowObservation is made ONLY by the trusted gateway from its authenticated
// configured Garm reader. Never decode it from provider/admin/operator JSON.
// It is current row metadata, NOT whole-host coverage, GitHub/job fence or permit.
type GarmRowObservation struct {
	RequestKey             string
	SourceScopeFingerprint string
	InstanceID             string
	Name                   string
	PoolID                 string
	ProviderName           string
	AgentID                int64
	Generation             uint64
	ObservedAt             time.Time
	PreparationShape       bool // exact pending-runner/no-job/provider shape; not activation
	CreationEligible       bool // includes current pool activation, separate from identity
}
type preparedDispatch struct {
	Receipt OriginalPreparationReceipt
	Row     GarmRowObservation
}

func sqlitePreparedDispatch(tx *sql.Tx, req Request, binding *HostBinding, plan *ExecutionPlan, prepared *preparedDispatch, now time.Time, maxAge time.Duration) error {
	if binding == nil || plan == nil || prepared == nil {
		return ErrOriginalPreparation
	}
	stored, err := originalPreparationRecord(tx, req.Key)
	if err != nil {
		return ErrOriginalPreparation
	}
	if err := preparationUncancelled(tx, req.Key); err != nil {
		return err
	}
	a, errA := json.Marshal(stored)
	b, errB := json.Marshal(prepared.Receipt)
	digest, err := executionPlanDigest(*plan)
	if errA != nil || errB != nil || string(a) != string(b) || err != nil || stored.Request != req || stored.HostBindingSequence != binding.Sequence || stored.PlanDigest != digest {
		return ErrOriginalPreparation
	}
	row := prepared.Row
	if row.RequestKey != req.Key || row.SourceScopeFingerprint != stored.Source.SourceScopeFingerprint || row.InstanceID != stored.Original.InstanceID || row.Name != stored.Original.Name || row.PoolID != stored.Original.PoolID || row.ProviderName != stored.Original.ProviderName || row.AgentID != stored.Original.AgentID || row.Generation != stored.Original.Generation || row.ObservedAt.IsZero() || row.ObservedAt.After(now) || now.Sub(row.ObservedAt) > maxAge {
		return ErrOriginalPreparation
	}
	return nil
}

// ExecutePreparedDispatch compares exact receipt, plan, row and backend binding
// in the same one-shot intent transaction. Metadata acknowledgement is not enough.
func (s *SQLiteStore) ExecutePreparedDispatch(ctx context.Context, plan ExecutionPlan, binding HostBinding, receipt OriginalPreparationReceipt, row GarmRowObservation, execute func(context.Context, DispatchPermit) (DispatchReceipt, error)) (DispatchRecord, error) {
	if !validHostBinding(binding) || binding.Sequence <= 0 || binding.Host != plan.Request.Host || plan.HostBindingSequence != binding.Sequence {
		return DispatchRecord{}, ErrHostBinding
	}
	return s.executeDispatch(ctx, plan.Request, &binding, &plan, &preparedDispatch{Receipt: receipt, Row: row}, execute)
}
