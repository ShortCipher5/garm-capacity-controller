package cihealth

// AttemptKey, JobKey and StepKey are the single identity surface shared by
// every per-entity CI record this control plane retains or attaches:
//
//   - Plan 05 retains timing and result history (AttemptFacts and its
//     children) keyed by these identities;
//   - verified per-job resource usage attaches through the existing
//     JobResourceScope, JobResourceSample and StockJobResourceBinding
//     surfaces, all keyed by JobKey;
//   - independent test-level results (Plan 06) attach through RecordRef.
//
// All of these records address the same exact source, repository, workflow,
// run, attempt, job and step identities, so they can be joined without
// renaming or re-typing identity. Display names and matrix labels are never
// identity and never appear in any key: two identically labeled jobs stay
// distinct rows because only their numeric IDs identify them.

// RecordRef addresses one independently collected record to the exact
// identity retained history uses. Step is nil for job-level records such as
// verified usage measurements; step-level records such as test results set
// it. A ref is valid only with a valid job key and, when present, a step
// belonging to that same job.
type RecordRef struct {
	Job  JobKey
	Step *StepKey
}

func (r RecordRef) Valid() bool {
	if !validKey(r.Job.Attempt) || r.Job.JobID <= 0 {
		return false
	}
	return r.Step == nil || r.Step.Job == r.Job && r.Step.Number > 0
}
