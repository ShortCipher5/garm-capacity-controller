package brokerinput

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"strings"
	"testing"
)

func TestPolicyCapsNeverZeroIncreaseIdleOrForeignSource(t *testing.T) {
	for _, n := range []int64{0, 1, 2, 4, 5} {
		s, _, _ := ledgerFixture()
		s.Policy.Repositories[0].Pools[0].MaxRunners = n
		s.PoolProfiles = map[string]admission.BrokerPoolProfile{pool: {Pool: pool, Repository: repo, SourceIdentity: "source", Authority: admission.StockProviderAuthority{Controller: "controller", PoolID: pool, ProviderName: "capacity"}}}
		p := garmapi.ManagedPool{ID: pool, RepositoryID: repo, ProviderName: "capacity", Fingerprint: strings.Repeat("a", 64), MaxRunners: 4, MinIdleRunners: 2}
		out, e := policyCapTargets(s, []garmapi.ManagedPool{p}, "source", "controller")
		if e != nil || len(out) != 1 {
			t.Fatal(out, e)
		}
		want := uint64(0)
		if n == 2 {
			want = 2
		}
		if out[0].maximum != want {
			t.Fatal(n, out)
		}
		if _, e = policyCapTargets(s, []garmapi.ManagedPool{p}, "foreign", "controller"); e == nil {
			t.Fatal("foreign origin")
		}
		if _, e = policyCapTargets(s, []garmapi.ManagedPool{p, p}, "source", "controller"); e == nil {
			t.Fatal("duplicate pool")
		}
	}
}
