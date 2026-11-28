package brokerinput

import (
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"sync"
	"testing"
)

func TestNamedGroupAdmissionAcrossRepositoriesAndRestart(t *testing.T) {
	ctx := t.Context()
	pools := []scPool{{scPoolA, scRepoA, false}, {scPoolB, scRepoB, false}}
	stock, trust, _ := scStock(t, pools, nil, nil)
	s, path := enrolledSharedCapacityStore(t, stock, trust, pools, nil)
	p, e := s.BrokerPolicy(ctx)
	if e != nil {
		t.Fatal(e)
	}
	p.Groups = []admission.ServerGroup{{ID: "33333333-3333-4333-8333-333333333333", Name: "Shared CI", Hosts: []string{"host"}, Repositories: []string{scRepoA}, Pools: []string{scPoolB}, Limit: admission.Resources{CPU: 1000, RAM: 1000, Runners: 1}}}
	if e = s.ReplaceBrokerPolicy(ctx, p); e != nil {
		t.Fatal(e)
	}
	if e = s.ReplaceBrokerPolicy(ctx, p); !errors.Is(e, admission.ErrBrokerConflict) {
		t.Fatalf("stale edit: %v", e)
	}
	other, e := admission.OpenSQLite(path, admission.Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	var wg sync.WaitGroup
	start := make(chan struct{})
	var first, second error
	batch := sharedGrantBatch(t, s, "group race", scPoolA)
	wg.Go(func() { <-start; _, first = s.IssueBrokerGrants(ctx, batch) })
	wg.Go(func() { <-start; _, second = other.AdmitPlacement(ctx, sharedPlacement(scPoolB, "competing")) })
	close(start)
	wg.Wait()
	if (first == nil) == (second == nil) {
		t.Fatalf("expected one accepted: %v %v", first, second)
	}
	after, e := other.ReadBrokerGrantState(ctx)
	if e != nil {
		t.Fatal(e)
	}
	charged, ok := after.Policy.Groups[0].Charged(after.Allocated)
	if !ok || charged != scSize || after.Allocated.Global != scSize {
		t.Fatalf("charge %+v", after.Allocated)
	}
	for _, id := range []string{scPoolA, scPoolB} {
		_, e := other.Admit(ctx, admission.Request{Key: "blocked", Pool: id, Host: "host", Resources: scSize})
		if !errors.Is(e, admission.ErrDenied) {
			t.Fatalf("direct over-cap admission %s: %v", id, e)
		}
	}
	p = after.Policy
	p.Groups = []admission.ServerGroup{}
	if e = other.ReplaceBrokerPolicy(ctx, p); !errors.Is(e, admission.ErrGroupMembership) {
		t.Fatalf("deleted charge: %v", e)
	}
	// Older clients needn't know the groups field, but cannot disable it.
	p.Groups = nil
	if e = other.ReplaceBrokerPolicy(ctx, p); e != nil {
		t.Fatal(e)
	}
	p, e = other.BrokerPolicy(ctx)
	if e != nil || len(p.Groups) != 1 {
		t.Fatalf("lost group %+v %v", p, e)
	}
}
