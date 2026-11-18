package brokerinput

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"reflect"
	"testing"
	"time"
)

const repo = "11111111-1111-4111-8111-111111111111"
const pool = "22222222-2222-4222-8222-222222222222"

func fixture() (capacitybroker.Snapshot, admission.BrokerPolicy, garmapi.DemandObservation, capacitybroker.Options) {
	n := time.Unix(1700000000, 0)
	size := capacitybroker.Resources{1000, 1000, 1000, 1}
	s := capacitybroker.Snapshot{Revision: "ledger", ObservedAt: n, Complete: true, Global: capacitybroker.Resources{2000, 2000, 2000, 2}, Hosts: map[string]capacitybroker.Host{"host": {Budget: capacitybroker.Resources{2000, 2000, 2000, 2}, AcceptNew: true}}, Repositories: map[string]capacitybroker.Repository{repo: {MaxRunners: 2}}, Pools: map[string]capacitybroker.Pool{pool: {Repository: repo, Size: size, Hosts: []string{"host"}, MaxRunners: 2}}}
	p := admission.BrokerPolicy{Revision: 1, Repositories: []admission.BrokerRepositoryPolicy{{ID: repo, Weight: 1, MaxRunners: 2, Pools: []admission.BrokerPoolPolicy{{ID: pool, MaxRunners: 2}}}}}
	d := garmapi.DemandObservation{StartedAt: n, FinishedAt: n, Digest: "stock", Pools: []garmapi.DiscoveredPool{{ManagedPool: garmapi.ManagedPool{ID: pool, RepositoryID: repo, Enabled: true}, OwnerKind: "repository", OwnerID: repo}}, Supply: []garmapi.RunnerSupply{{PoolID: pool}}, Jobs: []garmapi.QueuedJob{{WorkflowJobID: 1, RepositoryID: repo, CreatedAt: n, CandidatePoolIDs: []string{pool}}}}
	return s, p, d, capacitybroker.Options{Now: n, MaxAge: time.Minute, MaxAssignments: 100}
}
func TestStockJoinsPolicyAndDemand(t *testing.T) {
	s, p, d, o := fixture()
	plan, e := Stock(s, p, d, o)
	if e != nil || len(plan.Assignments) != 1 || plan.Assignments[0].Pool != pool || plan.Assignments[0].JobID != repo+":1" {
		t.Fatalf("%+v %v", plan, e)
	}
	if s.Pools[pool].Waiting != 0 || s.Pools[pool].MaxRunners != 2 {
		t.Fatal("mutated caller")
	}
	p.Revision++
	second, e := Stock(s, p, d, o)
	if e != nil || second.Revision == plan.Revision {
		t.Fatal("revision did not bind policy")
	}
}
func TestStockRejectsUnchargedSupplyAndWrongOwner(t *testing.T) {
	for _, change := range []func(*garmapi.DemandObservation){func(d *garmapi.DemandObservation) { d.Supply[0].Busy = 1 }, func(d *garmapi.DemandObservation) { d.Pools[0].OwnerID = "other" }, func(d *garmapi.DemandObservation) { d.Supply = nil }, func(d *garmapi.DemandObservation) { d.Jobs[0].CandidatePoolIDs = []string{"missing"} }} {
		s, p, d, o := fixture()
		change(&d)
		if _, e := Stock(s, p, d, o); e == nil {
			t.Fatal("accepted inconsistent state")
		}
	}
}
func TestStockDisabledPoolAndUnknownChargesRemain(t *testing.T) {
	s, p, d, o := fixture()
	d.Pools[0].Enabled = false
	plan, e := Stock(s, p, d, o)
	if e != nil || len(plan.Assignments) != 0 || len(plan.RemainingJobs) != 1 {
		t.Fatalf("%+v %v", plan, e)
	}
	d.Pools[0].Enabled = true
	s.Charges = []capacitybroker.Charge{{ID: "unknown", Host: "host", Pool: pool, Resources: capacitybroker.Resources{2000, 2000, 2000, 2}}}
	plan, e = Stock(s, p, d, o)
	if e != nil || len(plan.Assignments) != 0 {
		t.Fatalf("unknown not charged %+v %v", plan, e)
	}
}
func TestStockIdleMatchDoesNotMutateInputs(t *testing.T) {
	s, p, d, o := fixture()
	s.Charges = []capacitybroker.Charge{{ID: "idle", Host: "host", Pool: pool, Resources: s.Pools[pool].Size}}
	d.Supply[0].Idle = 1
	want := s.Pools[pool]
	plan, e := Stock(s, p, d, o)
	if e != nil || len(plan.CoveredByIdle) != 1 || len(plan.Assignments) != 0 || !reflect.DeepEqual(want, s.Pools[pool]) {
		t.Fatalf("%+v %v", plan, e)
	}
}

func TestStockDoesNotHideFutureCapacityTimestamp(t *testing.T) {
	s, p, d, o := fixture()
	s.ObservedAt = o.Now.Add(time.Second)
	if _, e := Stock(s, p, d, o); e == nil {
		t.Fatal("accepted future ledger snapshot")
	}
	s.ObservedAt = time.Time{}
	if _, e := Stock(s, p, d, o); e == nil {
		t.Fatal("accepted unknown snapshot time")
	}
}
