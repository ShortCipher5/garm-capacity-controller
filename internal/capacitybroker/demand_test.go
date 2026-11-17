package capacitybroker

import (
	"reflect"
	"testing"
)

func TestDemandMatchingReassignsFlexibleJob(t *testing.T) {
	jobs := []DemandJob{{"a", []string{"p", "q"}}, {"b", []string{"p"}}, {"c", []string{"p", "q"}}}
	got, err := MatchDemand(jobs, map[string]int{"p": 1, "q": 1})
	if err != nil || len(got.Covered) != 2 || got.Covered["a"] != "q" || got.Covered["b"] != "p" || len(got.Unmet) != 1 || len(got.Unmet[0].Candidates) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	reverse := []DemandJob{jobs[2], jobs[1], jobs[0]}
	again, err := MatchDemand(reverse, map[string]int{"q": 1, "p": 1})
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("unstable %+v %v", again, err)
	}
	if !reflect.DeepEqual(jobs[0].Candidates, []string{"p", "q"}) {
		t.Fatal("mutated input")
	}
}
func TestDemandMatchingRejectsAmbiguity(t *testing.T) {
	for _, jobs := range [][]DemandJob{{{"x", []string{"p", "p"}}}, {{"x", []string{"missing"}}}, {{"x", nil}, {"x", nil}}} {
		if _, err := MatchDemand(jobs, map[string]int{"p": 1}); err == nil {
			t.Fatal("accepted invalid jobs")
		}
	}
	for _, supply := range []map[string]int{{"p": -1}, {"p": 1001}, {"": 1}} {
		if _, err := MatchDemand(nil, supply); err == nil {
			t.Fatal("accepted invalid supply")
		}
	}
}
func TestDemandMatchingNoSupplyRetainsDemand(t *testing.T) {
	got, err := MatchDemand([]DemandJob{{"x", []string{"p", "q"}}, {"y", nil}}, map[string]int{"p": 0, "q": 0})
	if err != nil || len(got.Covered) != 0 || len(got.Unmet) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
}
