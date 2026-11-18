package capacitybroker

import (
	"fmt"
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

func TestDemandMatchingBudgetsEmptyPoolVisits(t *testing.T) {
	idle := map[string]int{}
	candidates := []string{}
	for i := 0; i < 999; i++ {
		id := fmt.Sprintf("p%03d", i)
		idle[id] = 0
		candidates = append(candidates, id)
	}
	idle["z"] = 1
	candidates = append(candidates, "z")
	jobs := make([]DemandJob, 1000)
	for i := range jobs {
		jobs[i] = DemandJob{ID: fmt.Sprintf("j%04d", i), Candidates: candidates}
	}
	got, err := MatchDemand(jobs, idle)
	if err != ErrSnapshot || got.Covered != nil || got.Unmet != nil {
		t.Fatalf("expected bounded failure without partial result: %+v %v", got, err)
	}
}
func TestDemandMatchingBudgetsDenseSlotVisits(t *testing.T) {
	jobs := make([]DemandJob, 400)
	for i := range jobs {
		jobs[i] = DemandJob{ID: fmt.Sprintf("j%04d", i), Candidates: []string{"p"}}
	}
	got, err := MatchDemand(jobs, map[string]int{"p": 100})
	if err != ErrSnapshot || got.Covered != nil || got.Unmet != nil {
		t.Fatalf("expected bounded failure without partial result: %+v %v", got, err)
	}
}
