package admission

import (
	"context"
	"encoding/json"
	"testing"
)

func TestStockGrantSelectionSkipsUnusableFirstGrantWithoutReleasingIt(t *testing.T) {
	for _, reason := range []string{"disabled", "unhealthy", "binding", "host-budget", "pool-hosts"} {
		t.Run(reason, func(t *testing.T) {
			s := placementFixture(t)
			ctx := context.Background()
			issueStockTestGrant(t, s, "one")
			issueStockTestGrant(t, s, "two")
			state, e := s.ReadBrokerGrantState(ctx)
			if e != nil {
				t.Fatal(e)
			}
			bad, good := state.Grants[0], state.Grants[1]
			if bad.Spec.Host == good.Spec.Host {
				t.Fatal("fixture hosts same")
			}
			if reason == "binding" {
				if _, e = s.db.Exec("DROP TRIGGER host_bindings_immutable_update; UPDATE host_bindings SET bound_sequence=? WHERE host=?", state.Sequence, bad.Spec.Host); e != nil {
					t.Fatal(e)
				}
			} else {
				c := state.Config
				h := c.Hosts[bad.Spec.Host]
				switch reason {
				case "disabled":
					h.Mode = "disabled"
				case "unhealthy":
					h.Healthy = false
				case "host-budget":
					h.Budget.CPU = bad.Spec.Resources.CPU - 1
				case "pool-hosts":
					p := c.Pools[bad.Spec.Authority.PoolID]
					p.Hosts = []string{good.Spec.Host}
					c.Pools[bad.Spec.Authority.PoolID] = p
				}
				c.Hosts[bad.Spec.Host] = h
				// Atomic fixture-only policy change with fresh observations; production
				// configuration changes close readiness and require new reconciliation.
				raw, _ := json.Marshal(c)
				if _, e = s.db.Exec("UPDATE metadata SET config=?", raw); e != nil {
					t.Fatal(e)
				}
			}
			plan, e := s.AdmitStockProviderPlacement(ctx, placementRequest("actual"), stockAuthority(), stockDigest())
			if e != nil || plan.Request.Host != good.Spec.Host {
				t.Fatalf("selected %+v error %v", plan, e)
			}
			after, e := s.ReadBrokerGrantState(ctx)
			if e != nil {
				t.Fatal(e)
			}
			unused := 0
			for _, g := range after.Grants {
				if g.State == "unused" {
					unused++
					if g.ID != bad.ID {
						t.Fatal("wrong grant retained")
					}
				}
			}
			if unused != 1 {
				t.Fatal("bad grant released")
			}
			snapshot, e := s.ReadSnapshot(ctx)
			if e != nil || snapshot.Allocated.Global.Runners != 2 {
				t.Fatal("charge lost", e)
			}
		})
	}
}
