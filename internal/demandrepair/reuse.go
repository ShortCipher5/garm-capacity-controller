package demandrepair

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/reuse"
)

// ReuseWorkloadProof converts one positive scoped lifecycle observation into
// the verified reuse workload proof, binding it to the exact stock
// registration. An unscoped observation, a queued or in-progress assignment
// or a runner identity that does not match the registration never converts:
// a busy, possibly live runner stays charged.
func (o JobLifecycleObservation) ReuseWorkloadProof(stock reuse.StockBinding) (reuse.WorkloadProof, error) {
	if !o.Valid() {
		return reuse.WorkloadProof{}, reuse.ErrTeardownEvidence
	}
	record := o.Record()
	runner := record.Runner
	if runner.RunnerID != stock.RunnerID || runner.Name != stock.RunnerName || runner.GitHubURL != stock.GitHubURL ||
		runner.ControllerID != stock.ControllerID || runner.PoolID != stock.PoolID {
		return reuse.WorkloadProof{}, reuse.ErrTeardownEvidence
	}
	proof := reuse.WorkloadProof{
		RepositoryHost:     record.Repository.Host,
		RepositoryOwner:    record.Repository.Owner,
		RepositoryName:     record.Repository.Name,
		JobID:              record.JobID,
		RunID:              record.RunID,
		RunAttempt:         record.RunAttempt,
		RunnerID:           record.RunnerID,
		RunnerName:         record.RunnerName,
		StartedAt:          record.StartedAt,
		ObservedAt:         record.ObservedAt,
		RegistrationDigest: stock.RegistrationDigest,
		SourceDigest:       record.SourceIdentity,
	}
	if record.CompletedAt != nil {
		proof.CompletedAt = *record.CompletedAt
		proof.Conclusion = record.Conclusion
	}
	if !proof.Valid() {
		return reuse.WorkloadProof{}, reuse.ErrTeardownEvidence
	}
	return proof, nil
}
