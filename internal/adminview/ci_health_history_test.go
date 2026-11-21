package adminview

import (
	"encoding/json"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

func TestCIHealthHistoryProjectionShowsExactCompletedCoverage(t *testing.T) {
	in := cihealth.RetainedTrendWindow{Initial: cihealth.RetainedTrendBucket{
		Runs:     2,
		Attempts: cihealth.RetainedTrendMetric{Observed: 2, Completed: 2, Success: 1, Failure: 1},
		Jobs: cihealth.RetainedTrendMetric{Observed: 3, Completed: 2, Success: 1, Failure: 1, Other: 1,
			Duration: &cihealth.TrendDurations{Samples: 1}},
	}}
	raw, err := json.Marshal(ciHistoryWindowView(in))
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Initial map[string]json.RawMessage `json:"initial"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"attempts": `{"observed":2,"success":1,"failure":1,"other":0,"active":0,"unknown":0,"completed":2}`,
		"jobs":     `{"observed":3,"success":1,"failure":1,"other":1,"active":0,"unknown":0,"completed":2,"duration":{"samples":1,"mean_ns":"0","p95_ns":"0"}}`,
		"steps":    `{"observed":0,"success":0,"failure":0,"other":0,"active":0,"unknown":0,"completed":0}`,
	} {
		if got := string(wire.Initial[key]); got != want {
			t.Errorf("%s: got %s; want %s", key, got, want)
		}
	}
}
