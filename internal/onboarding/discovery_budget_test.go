package onboarding

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

// stallingDiscovery blocks its identity read until the read context ends while
// answering every other section immediately, so a Discover run must finish just
// after ONE 5s budget instead of spending a budget on each healthy section.
type stallingDiscovery struct{ identityCalls atomic.Int32 }

func (s *stallingDiscovery) PhysicalIdentity(ctx context.Context) (incusapi.PhysicalServerIdentity, error) {
	s.identityCalls.Add(1)
	<-ctx.Done()
	return incusapi.PhysicalServerIdentity{}, ctx.Err()
}
func (s *stallingDiscovery) HostTotals(context.Context) (incusapi.HostTotals, error) {
	return incusapi.HostTotals{CPUMillisTotal: 8000, RAMMiBTotal: 32768, DiskMiBTotal: 524288}, nil
}
func (s *stallingDiscovery) Guests(context.Context) ([]incusapi.Guest, error) {
	return []incusapi.Guest{{Name: "runner-1", Status: "Running", Controller: "ctl-1"}}, nil
}
func (s *stallingDiscovery) SampleUtilization(context.Context) (incusapi.UtilizationSample, error) {
	return incusapi.UtilizationSample{CPUUsedMillis: 100, RAMUsedMiB: 200, DiskUsedMiB: 300, Guests: 1}, nil
}

func TestDiscoveryEnforcesFiveSecondBudgetPerCall(t *testing.T) {
	store := storeFixture(t)
	source := &stallingDiscovery{}
	manager, err := New(store, map[string]Observer{"candidate-b": source}, true)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, store)
	start := time.Now()
	result, err := manager.Discover(context.Background(), Request{requestID, "candidate-b", "budget enforcement check"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	// The stalled identity read must be cut off by its own 5s budget: the run
	// waited for that budget (not returning instantly) yet finished well before
	// the ~20s four stalled sections would take, proving the remaining sections
	// kept their own fresh budgets and answered immediately.
	if elapsed < 4500*time.Millisecond || elapsed > 15*time.Second {
		t.Fatalf("per-call budget not enforced: run took %s", elapsed)
	}
	if source.identityCalls.Load() != 1 {
		t.Fatalf("identity read repeated: %d", source.identityCalls.Load())
	}
	if result.Identity.ObservationAvailable || result.Identity.ServerVersion != "" || result.Identity.BindingClass != "not_checked" {
		t.Fatalf("stalled identity presented as current: %+v", result.Identity)
	}
	if !containsString(result.Blockers, "authenticated_identity_unavailable") {
		t.Fatalf("blockers: %v", result.Blockers)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "stale identity") {
		t.Fatalf("stale identity warning missing: %v", result.Warnings)
	}
	// The healthy sections must still aggregate behind the stalled identity.
	if result.Totals == nil || result.Totals.CPUMillis == nil || *result.Totals.CPUMillis != 8000 || len(result.Guests) != 1 || !result.Guests[0].Managed || result.Utilization == nil || result.Utilization.Guests != 1 {
		t.Fatalf("healthy sections degraded with identity: %+v", result)
	}
	if result.SuggestedBudget.CPU != 6000 || result.SuggestedBudget.RAM != 30720 {
		t.Fatalf("suggested budget: %+v", result.SuggestedBudget)
	}
	after := snapshot(t, store)
	unchangedExceptAudit(t, before, after)
	if len(after.Audit) != len(before.Audit)+2 {
		t.Fatal("lost request/completed discovery audit")
	}
}
