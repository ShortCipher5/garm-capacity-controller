package adminview

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

func TestCIHealthTimingProjectionPreservesPrecisionAndUnknown(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC)
	attempt := cihealth.AttemptKey{Source: "github", RepositoryID: 3, WorkflowID: 8, RunID: math.MaxInt64, Attempt: 2}
	job := cihealth.JobKey{Attempt: attempt, JobID: math.MaxInt64 - 1}
	a := cihealth.AttemptFacts{Key: attempt, AsOf: now, Clock: cihealth.Clock{Phase: cihealth.Completed}, Jobs: []cihealth.JobFacts{{
		Key: job, FirstQueuedObservedAt: now.Add(-2 * time.Second),
		Clock: cihealth.Clock{Phase: cihealth.Completed, StartedAt: now.Add(-time.Second), CompletedAt: now},
		Steps: []cihealth.StepFacts{
			{Key: cihealth.StepKey{Job: job, Number: 1}, Clock: cihealth.Clock{Phase: cihealth.Skipped}},
			{Key: cihealth.StepKey{Job: job, Number: 2}, Clock: cihealth.Clock{Phase: cihealth.Completed, StartedAt: now, CompletedAt: now}},
		},
	}}}
	view, err := projectCIHealthTiming([]ciViewScope{{"github", 3}}, []cihealth.AttemptFacts{a})
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{
		`"run_id":"9223372036854775807"`, `"attempt":"2"`, `"id":"9223372036854775806"`,
		`"queue":{"basis":"observed_lower_bound","nanoseconds":"1000000000"}`,
		`{"number":"1","status":"skipped"}`, `{"number":"2","status":"completed","execution":{"basis":"observed_endpoints","nanoseconds":"0"}}`,
	} {
		if !strings.Contains(string(got), needle) {
			t.Errorf("missing %s in %s", needle, got)
		}
	}
	if strings.Contains(string(got), `"wall"`) {
		t.Fatalf("missing attempt timing became a value: %s", got)
	}
}

func TestCIHealthTimingProjectionRefusesForeignOrOverflowingBatch(t *testing.T) {
	now := time.Now().UTC()
	valid := cihealth.AttemptFacts{Key: cihealth.AttemptKey{Source: "github", RepositoryID: 3, WorkflowID: 8, RunID: 9, Attempt: 1}, AsOf: now}
	foreign := valid
	foreign.Key.RepositoryID = 4
	tooManyJobs := valid
	tooManyJobs.Jobs = make([]cihealth.JobFacts, maxCIJobs+1)
	nonUTCYear := valid
	nonUTCYear.Key.Attempt++
	nonUTCYear.AsOf = time.Date(9999, 12, 31, 23, 30, 0, 0, time.FixedZone("west", -3600))
	tooManySteps := valid
	tooManySteps.Jobs = []cihealth.JobFacts{{Steps: make([]cihealth.StepFacts, maxCISteps+1)}}
	for _, facts := range [][]cihealth.AttemptFacts{
		{valid, foreign}, {valid, valid}, {valid, nonUTCYear}, {tooManyJobs}, {tooManySteps}, make([]cihealth.AttemptFacts, maxCIAttempts+1),
	} {
		got, err := projectCIHealthTiming([]ciViewScope{{"github", 3}}, facts)
		if !errors.Is(err, errCIProjection) || got.Attempts != nil {
			t.Fatalf("partially returned invalid batch: %+v %v", got, err)
		}
	}
	got, err := projectCIHealthTiming(nil, nil)
	if err != nil || got.Attempts == nil || len(got.Attempts) != 0 {
		t.Fatalf("empty result: %+v %v", got, err)
	}
}
