package managedgarm

import (
	"context"
	"os"
	"testing"
)

func TestRetainedStockReadRefusesBeforeRuntime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []context.Context{nil, ctx} {
		if got, err := retainStockReadOnly(c); err == nil || got != nil {
			t.Fatal("invalid context accepted")
		}
	}
	var absent *retainedStockRead
	absent.close()
	if absent.recheck(ctx) == nil {
		t.Fatal("nil binding accepted")
	}
	f, err := os.CreateTemp(t.TempDir(), "not-nsfs")
	if err != nil {
		t.Fatal(err)
	}
	if stockReadNamespaceMatches(f, stockReadSample{start: 1, net: "net:[1]"}) {
		t.Fatal("ordinary file accepted")
	}
	r := &retainedStockRead{namespace: f}
	if r.recheck(ctx) == nil {
		t.Fatal("canceled accepted")
	}
	if r.namespace != nil {
		t.Fatal("failure did not revoke")
	}
	if _, err := f.Stat(); err == nil {
		t.Fatal("failure did not close descriptor")
	}
	r.close()
}
func TestRetainedStockReadLifetimeDrift(t *testing.T) {
	p, _ := stockReadFixture()
	a := stockReadObservation{profile: p, pids: [3]int{101, 102, 103}, samples: [3]stockReadSample{{start: 1, net: "net:[1]", pid: "pid:[2]"}, {start: 2}, {start: 3}}}
	if !stockReadSameLifetime(a, a) {
		t.Fatal("equal sampled identity rejected")
	}
	for _, mutate := range []func(*stockReadObservation){
		func(b *stockReadObservation) { b.pids[0]++ },
		func(b *stockReadObservation) { b.samples[0].start++ },
		func(b *stockReadObservation) { b.samples[1].net = "net:[9]" },
		func(b *stockReadObservation) { b.samples[2].pid = "pid:[9]" },
		func(b *stockReadObservation) { b.profile.Directory = "/other" },
	} {
		b := a
		mutate(&b)
		if stockReadSameLifetime(a, b) {
			t.Fatal("drift accepted")
		}
	}
}
