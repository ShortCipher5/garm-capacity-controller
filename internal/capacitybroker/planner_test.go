package capacitybroker

import (
	"math"
	"reflect"
	"testing"
	"time"
)

func fixture() (Snapshot, Options) {
	now := time.Unix(1700000000, 0)
	s := Snapshot{Revision: "r1", ObservedAt: now, Complete: true, Global: Resources{6000, 6000, 6000, 6}, Hosts: map[string]Host{"h": {Budget: Resources{6000, 6000, 6000, 6}, AcceptNew: true}}, Repositories: map[string]Repository{"a": {MaxRunners: 6}, "b": {MaxRunners: 6}}, Pools: map[string]Pool{}}
	for _, id := range []string{"a", "b"} {
		s.Pools[id] = Pool{Repository: id, Size: Resources{1000, 1000, 1000, 1}, Hosts: []string{"h"}, MaxRunners: 6, Waiting: 6, OldestWaiting: now.Add(-time.Minute)}
	}
	return s, Options{Now: now, MaxAge: time.Minute, MaxAssignments: 100}
}
func counts(p Plan) map[string]int {
	r := map[string]int{}
	for _, a := range p.Assignments {
		r[a.Pool]++
	}
	return r
}
func TestEqualAndWeightedCapacity(t *testing.T) {
	s, o := fixture()
	p, e := Build(s, o)
	if e != nil || !reflect.DeepEqual(counts(p), map[string]int{"a": 3, "b": 3}) {
		t.Fatalf("%+v %v", p, e)
	}
	s.Repositories["b"] = Repository{Weight: 2, MaxRunners: 6}
	p, e = Build(s, o)
	if e != nil || !reflect.DeepEqual(counts(p), map[string]int{"a": 2, "b": 4}) {
		t.Fatalf("%+v %v", p, e)
	}
}
func TestOutstandingUnknownAndDeletingChargesAreNeverReleased(t *testing.T) {
	s, o := fixture()
	s.Charges = []Charge{{ID: "unknown", Host: "h", Pool: "a", Resources: Resources{4000, 4000, 4000, 4}}, {ID: "deleting", Host: "h", Pool: "b", Resources: Resources{2000, 2000, 2000, 2}}}
	p, e := Build(s, o)
	if e != nil || len(p.Assignments) != 0 {
		t.Fatalf("%+v %v", p, e)
	}
	// Lower desired pool cap doesn't free anything.
	q := s.Pools["a"]
	q.MaxRunners = 0
	s.Pools["a"] = q
	p, e = Build(s, o)
	if e != nil || len(p.Assignments) != 0 {
		t.Fatalf("%+v %v", p, e)
	}
	// Only the input adapter's actual qualified release can remove a charge.
	s.Charges[0].Resources = Resources{3000, 3000, 3000, 3}
	p, e = Build(s, o)
	if e != nil || counts(p)["b"] != 1 {
		t.Fatalf("%+v %v", p, e)
	}
}
func TestWarmOnlyAfterQueuedAndAlreadyIdleCounts(t *testing.T) {
	s, o := fixture()
	a := s.Pools["a"]
	a.Waiting = 0
	a.WarmIdle = 6
	s.Pools["a"] = a
	p, e := Build(s, o)
	if e != nil || counts(p)["b"] != 6 {
		t.Fatalf("%+v %v", p, e)
	}
	b := s.Pools["b"]
	b.Waiting = 0
	s.Pools["b"] = b
	a.WarmIdle = 2
	a.AvailableIdle = 1
	s.Pools["a"] = a
	s.Charges = []Charge{{ID: "idle", Host: "h", Pool: "a", Resources: a.Size}}
	p, e = Build(s, o)
	if e != nil || len(p.Assignments) != 1 || !p.Assignments[0].Warm {
		t.Fatalf("%+v %v", p, e)
	}
}
func TestRepositoryCapSharedAcrossPools(t *testing.T) {
	s, o := fixture()
	a := s.Pools["a"]
	s.Pools["a2"] = a
	s.Repositories["a"] = Repository{MaxRunners: 2}
	p, e := Build(s, o)
	c := counts(p)
	if e != nil || c["a"]+c["a2"] != 2 || c["b"] != 4 {
		t.Fatalf("%+v %v", p, e)
	}
}
func TestDiskHostAndUnmanagedUsage(t *testing.T) {
	s, o := fixture()
	s.Charges = []Charge{{ID: "unmanaged", Host: "h", Resources: Resources{0, 0, 5000, 0}}}
	p, e := Build(s, o)
	if e != nil || len(p.Assignments) != 1 {
		t.Fatalf("%+v %v", p, e)
	}
	h := s.Hosts["h"]
	h.AcceptNew = false
	s.Hosts["h"] = h
	p, e = Build(s, o)
	if e != nil || len(p.Assignments) != 0 {
		t.Fatalf("%+v %v", p, e)
	}
}
func TestRejectInvalidIncompleteStaleAndOverflow(t *testing.T) {
	cases := []func(*Snapshot, *Options){
		func(s *Snapshot, o *Options) { s.Complete = false }, func(s *Snapshot, o *Options) { s.ObservedAt = o.Now.Add(-2 * time.Minute) }, func(s *Snapshot, o *Options) { s.ObservedAt = o.Now.Add(time.Second) }, func(s *Snapshot, o *Options) { s.Global[0] = -1 }, func(s *Snapshot, o *Options) { p := s.Pools["a"]; p.Size[2] = 0; s.Pools["a"] = p }, func(s *Snapshot, o *Options) { s.Charges = []Charge{{ID: "x", Host: "h"}, {ID: "x", Host: "h"}} }, func(s *Snapshot, o *Options) { s.Charges = []Charge{{ID: "x", Host: "missing"}} }, func(s *Snapshot, o *Options) {
			s.Charges = []Charge{{ID: "x", Host: "h", Resources: Resources{math.MaxInt64}}, {ID: "y", Host: "h", Resources: Resources{1}}}
		}, func(s *Snapshot, o *Options) { p := s.Pools["a"]; p.AvailableIdle = 1; s.Pools["a"] = p }, func(s *Snapshot, o *Options) { o.MaxAssignments = 10001 },
	}
	for i, mutate := range cases {
		s, o := fixture()
		mutate(&s, &o)
		if _, e := Build(s, o); e != ErrSnapshot {
			t.Fatalf("case %d: %v", i, e)
		}
	}
}
func TestBoundedDeterministicNoMutation(t *testing.T) {
	s, o := fixture()
	o.MaxAssignments = 2
	p, e := Build(s, o)
	again, e2 := Build(s, o)
	if e != nil || e2 != nil || !reflect.DeepEqual(p, again) || len(p.Assignments) != 2 || s.Pools["a"].Waiting != 6 {
		t.Fatalf("%+v %v %v", p, e, e2)
	}
}
func TestOverBudgetAndZeroCapacityDoNotGrant(t *testing.T) {
	s, o := fixture()
	s.Global[0] = 0
	p, e := Build(s, o)
	if e != nil || len(p.Assignments) != 0 {
		t.Fatalf("%+v %v", p, e)
	}
	s, o = fixture()
	s.Charges = []Charge{{ID: "existing", Host: "h", Resources: Resources{7000, 0, 0, 0}}}
	p, e = Build(s, o)
	if e != nil || len(p.Assignments) != 0 {
		t.Fatalf("%+v %v", p, e)
	}
}

func TestAssignmentsNeverExceedAnyDimension(t *testing.T) {
	for cpu := int64(1); cpu <= 8; cpu++ {
		for disk := int64(0); disk <= 8; disk++ {
			s, o := fixture()
			s.Global = Resources{cpu * 1000, 6000, disk * 1000, 6}
			a := s.Pools["a"]
			a.Size = Resources{2000, 1000, 2000, 1}
			s.Pools["a"] = a
			p, e := Build(s, o)
			if e != nil {
				t.Fatal(e)
			}
			used := Resources{}
			for _, grant := range p.Assignments {
				if !fits(used, grant.Resources, s.Global) {
					t.Fatalf("over budget %+v %+v", s.Global, p)
				}
				used, _ = add(used, grant.Resources)
			}
		}
	}
}

func TestPendingWarmGrantSatisfiesTargetAcrossFreshSnapshots(t *testing.T) {
	s, o := fixture()
	for id, p := range s.Pools {
		p.Waiting = 0
		p.WarmIdle = 1
		s.Pools[id] = p
	}
	p, e := Build(s, o)
	if e != nil || len(p.Assignments) != 2 {
		t.Fatalf("%+v %v", p, e)
	}
	for _, a := range p.Assignments {
		s.Charges = append(s.Charges, Charge{ID: "warm-" + a.Pool, Host: a.Host, Pool: a.Pool, Resources: a.Resources})
		policy := s.Pools[a.Pool]
		policy.PendingWarm++
		s.Pools[a.Pool] = policy
	}
	s.Revision = "r2"
	s.ObservedAt = s.ObservedAt.Add(time.Second)
	o.Now = s.ObservedAt
	p, e = Build(s, o)
	if e != nil || len(p.Assignments) != 0 {
		t.Fatalf("pending warm duplicated: %+v %v", p, e)
	}
	// A subsequent unresolved attempt still occupies both capacity and warm target.
	s.Revision = "r3"
	p, e = Build(s, o)
	if e != nil || len(p.Assignments) != 0 {
		t.Fatalf("unknown warm duplicated: %+v %v", p, e)
	}
	policy := s.Pools["a"]
	policy.PendingWarm = 0
	policy.AvailableIdle = 1
	s.Pools["a"] = policy
	p, e = Build(s, o)
	if e != nil || len(p.Assignments) != 0 {
		t.Fatalf("idle warm duplicated: %+v %v", p, e)
	}
	policy.PendingWarm = 1
	s.Pools["a"] = policy
	if _, e = Build(s, o); e != ErrSnapshot {
		t.Fatalf("double-counted warm coverage accepted: %v", e)
	}
}
