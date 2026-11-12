package retirement

import (
	"encoding/json"
	"sync"
	"testing"
)

// This is an adversarial requirements fixture, NOT an implementation or a
// qualification of GitHub scheduling, a guest supervisor or runner cancellation.
// Production has no receiver/constructor that can accept these local phases as
// no-new-job proof. Actual upstream shutdown semantics are documented separately.
func TestIdleSnapshotCannotFenceAcceptedRunningOrPostJobWork(t *testing.T) {
	for _, phase := range []string{"accepted-after-idle-read", "running", "post-job-cleanup", "listener-restarted", "old-session-reconnected", "shutdown-cancelled-worker"} {
		t.Run(phase, func(t *testing.T) {
			snapshot, now := fixture()
			// No Garm row/resource and fresh stopped NEW ledger claims still cannot prove
			// that a remote listener has no previously accepted or newly assigned work.
			before, _ := json.Marshal(snapshot)
			result, err := Assess(snapshot, "host-a", now)
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			externalPhase := "idle"
			changed := make(chan struct{})
			go func() { mu.Lock(); externalPhase = phase; mu.Unlock(); close(changed) }()
			<-changed
			mu.Lock()
			current := externalPhase
			mu.Unlock()
			if current == "idle" || !result.LedgerNewClaimsPaused || !hasBlocker(result, "job_assignment_fence_unqualified") || !hasBlocker(result, "confirmed_retirement_unimplemented") {
				t.Fatal("point-in-time idleness became job/retirement authority", result, current)
			}
			after, _ := json.Marshal(snapshot)
			if string(before) != string(after) {
				t.Fatal("assignment-race fixture changed accounting")
			}
		})
	}
}
