package cihealth

import (
	"encoding/json"
	"testing"
)

func TestRecordRefValidation(t *testing.T) {
	key := AttemptKey{Source: "fixture", RepositoryID: 9007199254740993, WorkflowID: 44, RunID: 9007199254740995, Attempt: 1}
	job := JobKey{Attempt: key, JobID: 7000000000000000003}
	step := StepKey{Job: job, Number: 2}
	for name, ref := range map[string]RecordRef{
		"job level":    {Job: job},
		"step level":   {Job: job, Step: &step},
		"no job":       {Job: JobKey{Attempt: key}},
		"bad attempt":  {Job: JobKey{Attempt: AttemptKey{Source: "fixture", RepositoryID: -1, WorkflowID: 44, RunID: 5, Attempt: 1}, JobID: 7}},
		"foreign step": {Job: job, Step: &StepKey{Job: JobKey{Attempt: key, JobID: 8}, Number: 2}},
		"zero step":    {Job: job, Step: &StepKey{Job: job}},
	} {
		want := name == "job level" || name == "step level"
		if got := ref.Valid(); got != want {
			t.Fatalf("%s: Valid() = %t, want %t", name, got, want)
		}
	}
}

// The shared identity must survive an exact JSON round trip: 64-bit IDs stay
// lossless numbers in Go encodings, and no display name or label can ride
// along as identity.
func TestRecordRefJSONRoundTripKeepsExactIdentity(t *testing.T) {
	key := AttemptKey{Source: "fixture", RepositoryID: 9007199254740993, WorkflowID: 44, RunID: 9007199254740995, Attempt: 3}
	step := StepKey{Job: JobKey{Attempt: key, JobID: 7000000000000000003}, Number: 9}
	ref := RecordRef{Job: step.Job, Step: &step}
	payload, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	var back RecordRef
	if err := json.Unmarshal(payload, &back); err != nil {
		t.Fatal(err)
	}
	if back.Job != ref.Job || back.Step == nil || *back.Step != step || !back.Valid() {
		t.Fatalf("identity did not round trip exactly: %+v -> %s -> %+v", ref, payload, back)
	}
}
