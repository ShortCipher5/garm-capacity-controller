package brokerinput

import (
	"crypto/sha256"
	"math"
	"testing"
	"time"
)

func changePlanFixture(delta int64) (scalingPreviewSample, scalingPreviewSample, stockChangePlanRequest, stockChangePlanOptions, time.Time) {
	now := time.Unix(1700000100, 0)
	scope := sha256.Sum256([]byte("same reviewed configuration, bindings and profiles"))
	makeSample := func(start time.Time) scalingPreviewSample {
		return scalingPreviewSample{scope: scope, StockScalingSample: StockScalingSample{source: "checked-source", sequence: 4, revision: 2, budget: 10, start: start, finish: start.Add(time.Second), expires: now.Add(20 * time.Second), pools: map[string]scalingPool{"pool": {repo: "repo", fingerprint: "checked-settings", enabled: true, current: 3, idle: 1, used: 1, cap: 8, target: 3 + delta}}, repoCaps: map[string]int64{"repo": 10}, order: []string{"pool"}, configuredMaximumFit: roomyMaximumFit("pool")}}
	}
	prior, current := makeSample(now.Add(-30*time.Second)), makeSample(now.Add(-10*time.Second))
	request := stockChangePlanRequest{PoolID: "pool", RepositoryID: "repo", Source: "checked-source", Fingerprint: "checked-settings", Sequence: 4, Revision: 2, Observed: 3, Delta: delta, Scope: scope}
	return prior, current, request, stockChangePlanOptions{EnableValidation: true, MaxStep: 1, Minimum: 1, Maximum: 8}, now
}
func TestStockChangePlanDefaultOffAndBothSignedDirections(t *testing.T) {
	for _, delta := range []int64{-1, 1} {
		prior, current, request, opt, now := changePlanFixture(delta)
		if _, e := validateStockChangePlan(prior, current, request, stockChangePlanOptions{}, now); e == nil {
			t.Fatal("zero options enabled")
		}
		plan, e := validateStockChangePlan(prior, current, request, opt, now)
		if e != nil || plan.Actionable || plan.Suggested != 3+delta || plan.Delta != delta || plan.Scope != current.scope || !plan.ExpiresAt.Equal(current.expires) {
			t.Fatal("diagnostic plan", plan, e)
		}
		want := "increase"
		if delta < 0 {
			want = "decrease"
		}
		if plan.Direction != want {
			t.Fatal("unsigned direction")
		}
		// A returned struct cannot alter private observations or turn off the guard.
		plan.Actionable = true
		plan.Suggested = 100
		again, e := validateStockChangePlan(prior, current, request, opt, now)
		if e != nil || again.Actionable || again.Suggested != 3+delta {
			t.Fatal("mutable validation state")
		}
	}
}
func TestStockChangePlanRefusesStaleMismatchedAndUnboundedRequests(t *testing.T) {
	changes := map[string]func(*scalingPreviewSample, *scalingPreviewSample, *stockChangePlanRequest, *stockChangePlanOptions, *time.Time){
		"disabled": func(_, _ *scalingPreviewSample, _ *stockChangePlanRequest, o *stockChangePlanOptions, _ *time.Time) {
			o.EnableValidation = false
		},
		"step-limit": func(_, _ *scalingPreviewSample, _ *stockChangePlanRequest, o *stockChangePlanOptions, _ *time.Time) {
			o.MaxStep = 2
		},
		"zero-direction": func(_, _ *scalingPreviewSample, r *stockChangePlanRequest, _ *stockChangePlanOptions, _ *time.Time) {
			r.Delta = 0
		},
		"large-direction": func(_, _ *scalingPreviewSample, r *stockChangePlanRequest, _ *stockChangePlanOptions, _ *time.Time) {
			r.Delta = math.MaxInt64
		},
		"source": func(_, _ *scalingPreviewSample, r *stockChangePlanRequest, _ *stockChangePlanOptions, _ *time.Time) {
			r.Source = "other"
		},
		"pool": func(_, _ *scalingPreviewSample, r *stockChangePlanRequest, _ *stockChangePlanOptions, _ *time.Time) {
			r.PoolID = "other"
		},
		"repository": func(_, _ *scalingPreviewSample, r *stockChangePlanRequest, _ *stockChangePlanOptions, _ *time.Time) {
			r.RepositoryID = "other"
		},
		"fingerprint": func(_, _ *scalingPreviewSample, r *stockChangePlanRequest, _ *stockChangePlanOptions, _ *time.Time) {
			r.Fingerprint = "other"
		},
		"sequence": func(_, _ *scalingPreviewSample, r *stockChangePlanRequest, _ *stockChangePlanOptions, _ *time.Time) {
			r.Sequence++
		},
		"revision": func(_, _ *scalingPreviewSample, r *stockChangePlanRequest, _ *stockChangePlanOptions, _ *time.Time) {
			r.Revision++
		},
		"observed-max": func(_, _ *scalingPreviewSample, r *stockChangePlanRequest, _ *stockChangePlanOptions, _ *time.Time) {
			r.Observed++
		},
		"scope": func(p, _ *scalingPreviewSample, _ *stockChangePlanRequest, _ *stockChangePlanOptions, _ *time.Time) {
			p.scope[0] ^= 1
		},
		"requested-scope": func(_, _ *scalingPreviewSample, r *stockChangePlanRequest, _ *stockChangePlanOptions, _ *time.Time) {
			r.Scope[0] ^= 1
		},
		"expired": func(_, c *scalingPreviewSample, _ *stockChangePlanRequest, _ *stockChangePlanOptions, n *time.Time) {
			c.expires = *n
		},
		"future": func(_, c *scalingPreviewSample, _ *stockChangePlanRequest, _ *stockChangePlanOptions, n *time.Time) {
			c.finish = n.Add(time.Second)
		},
		"overlap": func(p, c *scalingPreviewSample, _ *stockChangePlanRequest, _ *stockChangePlanOptions, _ *time.Time) {
			c.start = p.finish.Add(time.Second)
		},
		"incomplete": func(_, c *scalingPreviewSample, _ *stockChangePlanRequest, _ *stockChangePlanOptions, _ *time.Time) {
			c.incomplete = true
		},
		"limit": func(_, _ *scalingPreviewSample, _ *stockChangePlanRequest, o *stockChangePlanOptions, _ *time.Time) {
			o.Maximum = 3
		},
		"wrong-confirmed-direction": func(p, _ *scalingPreviewSample, _ *stockChangePlanRequest, _ *stockChangePlanOptions, _ *time.Time) {
			v := p.pools["pool"]
			v.target = 2
			p.pools["pool"] = v
		},
	}
	for name, mutate := range changes {
		t.Run(name, func(t *testing.T) {
			p, c, r, o, n := changePlanFixture(1)
			mutate(&p, &c, &r, &o, &n)
			if plan, e := validateStockChangePlan(p, c, r, o, n); e == nil || plan != (StockChangePlan{}) {
				t.Fatal("invalid diagnostic admitted", plan)
			}
		})
	}
}
func TestStockChangePlanMinimumIdleBusyAndNoBorrowedReductionHeadroom(t *testing.T) {
	for _, field := range []string{"idle", "busy", "minimum", "disabled"} {
		t.Run(field, func(t *testing.T) {
			p, c, r, o, n := changePlanFixture(-1)
			v := c.pools["pool"]
			switch field {
			case "idle":
				v.idle = 3
			case "busy":
				v.used = 3
			case "minimum":
				o.Minimum = 3
			case "disabled":
				v.enabled = false
			}
			c.pools["pool"] = v
			if _, e := validateStockChangePlan(p, c, r, o, n); e == nil {
				t.Fatal("floor or enablement bypass")
			}
		})
	}
	p, c, r, o, n := changePlanFixture(1)
	p.budget = 3
	c.budget = 3
	if _, e := validateStockChangePlan(p, c, r, o, n); e == nil {
		t.Fatal("global maximum headroom invented")
	}
}

func TestStockChangePlanUsesFullOrderedDiskVetoAndEarliestExpiry(t *testing.T) {
	h := diskPreviewFixture(t)
	prior, current := h[2], h[3]
	now := current.finish
	opt := stockChangePlanOptions{EnableValidation: true, MaxStep: 1, Minimum: 1, Maximum: 4}
	request := func(id string, delta int64) stockChangePlanRequest {
		p := current.pools[id]
		return stockChangePlanRequest{PoolID: id, RepositoryID: p.repo, Source: current.source, Fingerprint: p.fingerprint,
			Sequence: current.sequence, Revision: current.revision, Observed: p.current, Delta: delta, Scope: current.scope}
	}
	assertPlan := func(id string, delta int64, want time.Time) {
		t.Helper()
		plan, err := validateStockChangePlan(prior, current, request(id, delta), opt, now)
		if err != nil || plan.Actionable || !plan.ExpiresAt.Equal(want) || plan.Delta != delta {
			t.Fatalf("%q diagnostic = %+v, %v; want non-actionable and expiry %s", id, plan, err, want)
		}
	}
	assertRefused := func(id string) {
		t.Helper()
		if plan, err := validateStockChangePlan(prior, current, request(id, 1), opt, now); err != errStockChangePlan || plan != (StockChangePlan{}) {
			t.Fatalf("%q diagnostic survived a disk veto: %+v, %v", id, plan, err)
		}
	}

	diskExpiry := prior.disk.pending.expiresAt
	assertPlan("z", 1, diskExpiry)
	assertPlan("a", 1, diskExpiry)
	assertPlan("empty", -1, prior.expires)

	// A alone could fit. Z is first in weighted order and consumes the only
	// usable growth slot. Neither choosing A directly nor the pending cut can
	// supply more headroom.
	prior.disk.filesystem.AvailableInodes = 6
	bindEmptyDiskPreviewPending(t, prior.disk, diskExpiry)
	assertPlan("z", 1, diskExpiry)
	assertRefused("a")

	prior.disk.filesystem.AvailableInodes = 1
	bindEmptyDiskPreviewPending(t, prior.disk, diskExpiry)
	assertRefused("z")
	assertRefused("a")
	assertPlan("empty", -1, prior.expires)

	prior.disk.filesystem.AvailableInodes = 12
	bindEmptyDiskPreviewPending(t, prior.disk, diskExpiry)
	now = diskExpiry
	assertRefused("z") // Strict evidence expiry, not the later stock expiry.
	assertPlan("empty", -1, prior.expires)

	now = current.finish
	prior.diskRequired = false
	assertRefused("z") // Requiring the brake in only one capture fails closed.
	current.diskRequired = false
	assertPlan("z", 1, prior.expires) // Base diagnostic remains unchanged.
}
