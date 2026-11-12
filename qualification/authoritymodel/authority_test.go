// This package is a TEST-ONLY executable acceptance specification. It has no
// production adapter, HTTP endpoint, credential, signed receipt or activation API.
// A passing abstract model is NOT proof that Incus or an installation enforces it.
package authoritymodel

import (
	"errors"
	"sync"
	"testing"
)

var errBlocked = errors.New("external authority model blocked")

type token struct {
	epoch uint64
	scope string
}
type operation struct {
	scope      string
	units      int
	unresolved bool
}
type authority struct {
	mu         sync.Mutex
	epoch      uint64
	available  bool
	frozen     bool
	assets     map[string]string // daemon/project -> independently registered physical asset
	caps       map[string]int
	operations map[string]operation // accepted operations remain charged, even if reply lost
}

func fixture() *authority {
	return &authority{epoch: 1, available: true, assets: map[string]string{"daemon-a/project-a": "asset-1", "daemon-b/hidden-project": "asset-1"}, caps: map[string]int{"asset-1": 1}, operations: map[string]operation{}}
}

// The epoch check, duplicate decision, native-parent reservation and durable
// intent are ONE authority operation, not a client-side check followed by POST.
// A real remote Incus POST cannot be made atomic by copying this mutex model.
func (a *authority) commit(t token, id string, units int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.available || a.frozen || t.epoch != a.epoch || id == "" || units <= 0 {
		return errBlocked
	}
	asset, ok := a.assets[t.scope]
	if !ok {
		return errBlocked
	}
	if _, exists := a.operations[id]; exists {
		return errBlocked
	} // no duplicate physical attempt
	allocated := 0
	for _, op := range a.operations {
		if a.assets[op.scope] == asset {
			allocated += op.units
		}
	}
	if allocated+units > a.caps[asset] {
		return errBlocked
	}
	a.operations[id] = operation{scope: t.scope, units: units, unresolved: true}
	return nil
}

func (a *authority) fence() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.epoch++
	a.frozen = true
	return a.epoch
}
func (a *authority) observedTerminal(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	op := a.operations[id]
	op.unresolved = false
	a.operations[id] = op
}
func (a *authority) resume(epoch uint64, completeCoverage bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.available || epoch != a.epoch || !completeCoverage {
		return errBlocked
	}
	for _, op := range a.operations {
		if op.unresolved {
			return errBlocked
		}
	}
	a.frozen = false
	return nil
}

func TestModelOldTokenCannotCommitAfterFenceIncludingQueuedRequest(t *testing.T) {
	a := fixture()
	old := token{1, "daemon-a/project-a"}
	// Simulate a request checked/queued before rotation but not yet committed.
	if old.epoch != a.epoch {
		t.Fatal("fixture")
	}
	current := a.fence()
	if err := a.resume(current, true); err != nil {
		t.Fatal(err)
	}
	if a.commit(old, "queued-old", 1) == nil {
		t.Fatal("old queued handler bypassed final epoch check")
	}
	if len(a.operations) != 0 {
		t.Fatal("rejected old request consumed physical capacity")
	}
}

func TestModelAcceptedOldWorkAndLostReplyBlockResumeAndRemainCharged(t *testing.T) {
	a := fixture()
	if err := a.commit(token{1, "daemon-a/project-a"}, "accepted-old", 1); err != nil {
		t.Fatal(err)
	}
	current := a.fence()
	// A timeout, a lost reply, an empty instance list or old process death does not
	// resolve the durable accepted operation. This model has NO release primitive.
	if a.resume(current, true) == nil {
		t.Fatal("unknown accepted work was drained by assertion")
	}
	if len(a.operations) != 1 || a.operations["accepted-old"].units != 1 {
		t.Fatal("lost reply lost charge")
	}
	a.observedTerminal("accepted-old")
	if err := a.resume(current, true); err != nil {
		t.Fatal(err)
	}
	if a.commit(token{current, "daemon-b/hidden-project"}, "new", 1) == nil {
		t.Fatal("terminal old create was mistaken for physical absence")
	}
}

func TestModelRawSameIDSnapshotCannotRestoreExternalWriterEpoch(t *testing.T) {
	a := fixture()
	snapshotToken := token{1, "daemon-a/project-a"}
	epoch := a.fence()
	if err := a.resume(epoch, true); err != nil {
		t.Fatal(err)
	}
	// The same local ledger ID does not advance a separately durable epoch.
	if a.commit(snapshotToken, "raw-active-clone", 1) == nil {
		t.Fatal("raw clone regained writer authority")
	}
}

func TestModelPartitionAndIncompleteCoverageHaveNoCachedOrBooleanOverride(t *testing.T) {
	a := fixture()
	a.available = false
	if a.commit(token{1, "daemon-a/project-a"}, "partition", 1) == nil {
		t.Fatal("partition used stale grant")
	}
	epoch := a.fence()
	a.available = true
	if a.resume(epoch, false) == nil {
		t.Fatal("project-only inventory established whole-host completeness")
	}
	if a.commit(token{epoch, "daemon-a/project-a"}, "still-frozen", 1) == nil {
		t.Fatal("incomplete coverage unpaused writes")
	}
}

func TestModelTwoDaemonKeysAndProjectsShareOneAtomicPhysicalParentCap(t *testing.T) {
	a := fixture()
	var accepted int
	var mu sync.Mutex
	var group sync.WaitGroup
	for _, scope := range []string{"daemon-a/project-a", "daemon-b/hidden-project"} {
		group.Add(1)
		go func(scope string) {
			defer group.Done()
			if a.commit(token{1, scope}, scope, 1) == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}(scope)
	}
	group.Wait()
	if accepted != 1 || len(a.operations) != 1 {
		t.Fatal("per-daemon/project caps doubled one physical asset", accepted, len(a.operations))
	}
}

func TestCounterexampleCheckThenWriteIsNotBackendFencing(t *testing.T) {
	a := fixture()
	old := token{1, "daemon-a/project-a"}
	clientChecked := old.epoch == a.epoch
	a.fence()
	// An unsafe check-then-POST design has no final authority recheck. Observing
	// this counterexample is the expected result, not a production safety pass.
	unsafePhysicalWrites := 0
	if clientChecked {
		unsafePhysicalWrites++
	}
	if unsafePhysicalWrites != 1 {
		t.Fatal("counterexample fixture stopped demonstrating stale request")
	}
	if a.commit(old, "safe-path", 1) == nil {
		t.Fatal("specified authority failed to reject same request")
	}
}
