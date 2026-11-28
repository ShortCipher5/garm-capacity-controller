package admission

import "testing"

func TestGroupPartitionOwnsHostsAndPoolsExactlyOnce(t *testing.T) {
	g1 := "11111111-1111-4111-8111-111111111111"
	g2 := "22222222-2222-4222-8222-222222222222"
	r1 := "33333333-3333-4333-8333-333333333333"
	r2 := "44444444-4444-4444-8444-444444444444"
	p1 := "55555555-5555-4555-8555-555555555555"
	p2 := "66666666-6666-4666-8666-666666666666"
	policy := BrokerPolicy{Repositories: []BrokerRepositoryPolicy{{ID: r1, Weight: 1, Pools: []BrokerPoolPolicy{{ID: p1}}}, {ID: r2, Weight: 1, Pools: []BrokerPoolPolicy{{ID: p2}}}}, Groups: []ServerGroup{{ID: g1, Name: "West", Hosts: []string{"a", "b"}, Repositories: []string{r1}, Pools: []string{}, Limit: Resources{CPU: 10, RAM: 20, Runners: 2}}, {ID: g2, Name: "East", Hosts: []string{"c"}, Repositories: []string{}, Pools: []string{p2}, Limit: Resources{CPU: 10, RAM: 10, Runners: 2}}}}
	x, e := indexGroups(policy)
	if e != nil {
		t.Fatal(e)
	}
	a := Allocations{Hosts: map[string]Resources{"a": {CPU: 5, RAM: 6, Runners: 1}, "b": {CPU: 4, RAM: 4}}}
	if !x.allows("b", p1, a, Resources{CPU: 1, RAM: 10, Runners: 1}) || x.allows("b", p2, a, Resources{}) || x.allows("outside", p1, a, Resources{}) || !x.allows("outside", "unbound", a, Resources{}) {
		t.Fatal("membership/fit mismatch")
	}
	for _, tooMuch := range []Resources{{CPU: 2}, {RAM: 11}, {Runners: 2}} {
		if x.allows("a", p1, a, tooMuch) {
			t.Fatal("over cap", tooMuch)
		}
	}
	policy.Groups[1].Hosts = append(policy.Groups[1].Hosts, "a")
	if ValidateBrokerPolicy(policy) == nil {
		t.Fatal("accepted overlapping host")
	}
	policy.Groups[1].Hosts = []string{"c"}
	policy.Groups[1].Pools = append(policy.Groups[1].Pools, p1)
	if ValidateBrokerPolicy(policy) == nil {
		t.Fatal("accepted repository/explicit pool overlap")
	}
}
