package onboarding

import (
	"reflect"
	"testing"
)

func proposalInt(n int64) *int64 { return &n }
func proposalFixture() DiscoveryResult {
	return DiscoveryResult{Totals: &DiscoveryTotals{CPUMillis: proposalInt(12000), RAMMiB: proposalInt(24576), DiskMiB: proposalInt(240000), DiskMiBUsed: proposalInt(20000)}, Guests: []GuestView{}}
}
func TestProposalHeadroomAndGuestReservations(t *testing.T) {
	r := proposalFixture()
	p := ProposeBudget(r)
	if !reflect.DeepEqual(p.Budget, map[string]int64{"cpu_millis": 10800, "ram_mib": 22118, "disk_mib": 196000}) {
		t.Fatal(p)
	}
	r.Guests = []GuestView{{Managed: true, CPUMillis: proposalInt(2000), RAMMiB: proposalInt(4096)}}
	p = ProposeBudget(r)
	if p.Budget["cpu_millis"] != 8800 || p.Budget["ram_mib"] != 18022 {
		t.Fatal(p)
	}
	if _, ok := p.Budget["disk_mib"]; ok {
		t.Fatal("unknown guest disk fabricated")
	}
	r.Blockers = []string{"guest_inventory_unavailable"}
	if len(ProposeBudget(r).Budget) != 0 {
		t.Fatal("unknown inventory proposed budget")
	}
}
func TestProposalDiskGrowthAndPoolRemainConservative(t *testing.T) {
	r := proposalFixture()
	r.Totals.DiskMiB = proposalInt(204800)
	r.Totals.DiskMiBUsed = proposalInt(102400)
	r.Guests = []GuestView{{CPUMillis: proposalInt(1000), RAMMiB: proposalInt(2048), DiskMiBRoot: proposalInt(102400)}}
	if _, ok := ProposeBudget(r).Budget["disk_mib"]; ok {
		t.Fatal("used disk plus future growth not reserved")
	}
	r = proposalFixture()
	r.Totals.StoragePool = "pool"
	r.Totals.PoolDiskMiB = proposalInt(80000)
	r.Totals.PoolDiskUsed = proposalInt(40000)
	if ProposeBudget(r).Budget["disk_mib"] != 29760 {
		t.Fatal(ProposeBudget(r))
	}
	r.Totals.PoolDiskUsed = nil
	if _, ok := ProposeBudget(r).Budget["disk_mib"]; ok {
		t.Fatal("unknown pool usage fabricated")
	}
}
func TestProposalUnknownAndOutOfRangeNeverMeanUnlimited(t *testing.T) {
	for _, mutate := range []func(*DiscoveryResult){
		func(r *DiscoveryResult) { r.Totals.DiskMiBUsed = nil },
		func(r *DiscoveryResult) { r.Totals.DiskMiBUsed = proposalInt(240000) },
		func(r *DiscoveryResult) { r.Totals.DiskMiB = proposalInt(maxPlanningMagnitude + 1) },
		func(r *DiscoveryResult) {
			r.Guests = []GuestView{{DiskMiBRoot: proposalInt(maxPlanningMagnitude)}, {DiskMiBRoot: proposalInt(1)}}
		},
	} {
		r := proposalFixture()
		mutate(&r)
		if _, ok := ProposeBudget(r).Budget["disk_mib"]; ok {
			t.Fatal("unsafe disk proposal", ProposeBudget(r))
		}
	}
	if len(ProposeBudget(DiscoveryResult{}).Budget) != 0 {
		t.Fatal("missing totals fabricated")
	}
}
