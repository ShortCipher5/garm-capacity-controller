package brokerinput

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"testing"
	"time"
)

func TestReadPlanProjectionKeepsUnknownSeparate(t *testing.T) {
	state, _, opt := ledgerFixture()
	_, _, observation, _ := fixture()
	observation.Supply = nil
	plan := capacitybroker.JobPlan{Plan: capacitybroker.Plan{Assignments: []capacitybroker.Assignment{{Pool: pool, Warm: true}}}, CoveredByIdle: map[string]string{"a": pool}, CoveredByGrant: map[string]string{"b": pool}, DeferredByInflight: map[string]string{"c": pool}}
	out := projectReadPlan(state, observation, plan, opt.Now, time.Minute)
	if len(out.Pools) != 1 || out.Pools[0].Supply != nil || out.Pools[0].Additional != 1 || out.Pools[0].AdditionalWarm != 1 || out.Pools[0].CoveredByIdle != 1 || out.Pools[0].CoveredByGrant != 1 || out.Pools[0].DeferredByInflight != 1 || out.Atomic {
		t.Fatal(out)
	}
}
func TestReadPlanMissingSourceIsUnknown(t *testing.T) {
	var empty *Service
	if _, err := empty.ReadPlan(context.Background()); err == nil {
		t.Fatal("nil service claimed plan")
	}
	s := &Service{reader: &planReader{}}
	if _, err := s.ReadPlan(context.Background()); err == nil {
		t.Fatal("missing source claimed plan")
	}
}

func TestReadPlanExpiresWithOldestHostEvidence(t *testing.T) {
	state, _, opt := ledgerFixture()
	_, _, observed, _ := fixture()
	host := state.Config.Hosts["host"]
	host.ObservedAt = opt.Now.Add(-50 * time.Second)
	state.Config.Hosts["host"] = host
	out := projectReadPlan(state, observed, capacitybroker.JobPlan{}, opt.Now, time.Minute)
	if !out.ExpiresAt.Equal(opt.Now.Add(10 * time.Second)) {
		t.Fatal("stock recency hid old host evidence", out.ExpiresAt)
	}
}
