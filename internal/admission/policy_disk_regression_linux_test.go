package admission

import (
	"context"
	"testing"
	"time"
)

func TestPolicyEditPreservesExplicitDiskAndOtherBudgetDimensions(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	for _, scope := range []struct{ action, target string }{{"global_budget", ""}, {"host_budget", "a"}, {"pool_budget", "app"}} {
		original := Resources{CPU: 4000, RAM: 8192, Disk: 30720, Runners: 4}
		change := PolicyChange{Revision: PolicyRevision(sqliteSnapshot(t, s).Config), Action: scope.action, Target: scope.target, Budget: &original, Reason: "set explicit existing disk ceiling"}
		if err := s.ApplyPolicyChange(ctx, change); err != nil {
			t.Fatal(err)
		}
		edited := original
		edited.CPU = 3000
		change.Budget = &edited
		change.Revision = PolicyRevision(sqliteSnapshot(t, s).Config)
		if err := s.ApplyPolicyChange(ctx, change); err != nil {
			t.Fatal(err)
		}
		after := sqliteSnapshot(t, s)
		actual := after.Config.Global
		if scope.action == "host_budget" {
			actual = after.Config.Hosts[scope.target].Budget
		}
		if scope.action == "pool_budget" {
			actual = after.Config.Pools[scope.target].Budget
		}
		if actual != edited {
			t.Fatalf("%s lost explicit dimension: %+v", scope.action, actual)
		}
		if after.Ready {
			t.Fatal("editing budget reopened readiness")
		}
	}
}
