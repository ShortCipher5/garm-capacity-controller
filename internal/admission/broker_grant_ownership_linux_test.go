package admission

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// Concurrent retries may share immutable caller input. Decoding an owned copy
// must not write through the input slice (including nested execution profiles).
func TestBrokerGrantConcurrentReplayDoesNotMutateCallerBatch(t *testing.T) {
	store := placementFixture(t)
	ctx := context.Background()
	state, err := store.ReadBrokerGrantState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.LookupHostBinding(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	request := placementRequest("unused")
	request.Execution.Name = ""
	batch := BrokerGrantBatch{ID: strings.Repeat("e", 64), ExpectedSequence: state.Sequence, PolicyRevision: state.Policy.Revision, Grants: []BrokerGrantSpec{{Authority: stockAuthority(), Host: "a", HostBindingSequence: binding.Sequence, Resources: request.Resources, Execution: request.Execution, ExpiresAt: store.now().Add(time.Hour)}}}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := store.IssueBrokerGrants(ctx, batch); failures <- err }()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	after, err := store.ReadBrokerGrantState(ctx)
	if err != nil || len(after.Grants) != 1 || after.Allocated.Global.Runners != 1 {
		t.Fatalf("retry changed accounting: %+v %v", after, err)
	}
}
