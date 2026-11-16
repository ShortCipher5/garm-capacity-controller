package adminview

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"strings"
	"testing"
	"time"
)

type scaleDiscoveryFixture struct {
	poolErr, scaleErr bool
	calls             int
}

func (f *scaleDiscoveryFixture) DiscoverPools(context.Context) (garmapi.PoolDiscovery, error) {
	if f.poolErr {
		return garmapi.PoolDiscovery{}, errors.New("private upstream")
	}
	return garmapi.PoolDiscovery{Pools: []garmapi.DiscoveredPool{}, MissingConfiguredPoolIDs: []string{}}, nil
}
func (f *scaleDiscoveryFixture) DiscoverScaleSets(ctx context.Context) (garmapi.ScaleSetDiscovery, error) {
	f.calls++
	if _, ok := ctx.Deadline(); !ok {
		panic("missing deadline")
	}
	if f.scaleErr {
		return garmapi.ScaleSetDiscovery{}, errors.New("private upstream")
	}
	return garmapi.ScaleSetDiscovery{ScaleSets: []garmapi.DiscoveredScaleSet{}}, nil
}
func TestDiscoveryIndependentScaleSetAvailability(t *testing.T) {
	for _, tc := range []struct{ poolErr, scaleErr bool }{{false, false}, {false, true}, {true, false}, {true, true}} {
		f := &scaleDiscoveryFixture{poolErr: tc.poolErr, scaleErr: tc.scaleErr}
		v, _ := NewPoolDiscoveryView(f)
		s, err := v.Snapshot(context.Background())
		if tc.poolErr && tc.scaleErr {
			if err == nil {
				t.Fatal("both failures hidden")
			}
			continue
		}
		if err != nil || (s.PoolInventory == nil) != tc.poolErr || (s.ScaleSetInventory == nil) != tc.scaleErr {
			t.Fatalf("partial failure misrepresented %+v: %v", tc, err)
		}
		if s.WritesEnabled || s.SettingsAvailable || s.Settings.WritesEnabled || !s.DiscoveryOnly {
			t.Fatal("discovery expanded writes")
		}
		if f.calls != 1 {
			t.Fatal("retry")
		}
	}
	if observeScaleSets(context.Background(), &discoveryOnlyFixture{}) != nil {
		t.Fatal("unsupported became empty")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if observeScaleSets(ctx, &scaleDiscoveryFixture{}) != nil {
		t.Fatal("canceled became fresh")
	}
}

func TestScaleSetObservationReservesCallerBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	f := &deadlineScaleFixture{t: t, maximum: 600 * time.Millisecond}
	if observeScaleSets(ctx, f) == nil {
		t.Fatal("observation unavailable")
	}
}

type deadlineScaleFixture struct {
	t       *testing.T
	maximum time.Duration
}

func (f *deadlineScaleFixture) DiscoverScaleSets(ctx context.Context) (garmapi.ScaleSetDiscovery, error) {
	d, ok := ctx.Deadline()
	if !ok || time.Until(d) > f.maximum {
		f.t.Fatal("optional discovery consumed caller budget")
	}
	return garmapi.ScaleSetDiscovery{ScaleSets: []garmapi.DiscoveredScaleSet{}}, nil
}

func TestOptionalScaleSetInventoryCannotEvictEssentialOverview(t *testing.T) {
	view := GarmOverview{WriteBlocked: true, Pending: &GarmPending{RequestID: "held", Kind: "pool", Target: "existing"}, History: []GarmChangeEvent{}, Settings: garmapi.ManagementSnapshot{Pools: []garmapi.ManagedPool{{ID: "existing", ProviderName: strings.Repeat("p", 1300000)}}, Repositories: []garmapi.ManagedRepository{}}}
	before, err := json.Marshal(view)
	if err != nil || len(before) > 2<<20 {
		t.Fatal("invalid test baseline")
	}
	view.ScaleSetInventory = &garmapi.ScaleSetDiscovery{ScaleSets: []garmapi.DiscoveredScaleSet{{ID: 42, Name: strings.Repeat("s", 900000)}}}
	large, _ := json.Marshal(view)
	if len(large) <= 2<<20 {
		t.Fatal("did not cross aggregate cap")
	}
	result := withBoundedScaleSetInventory(view)
	after, err := json.Marshal(result)
	if err != nil || string(after) != string(before) {
		t.Fatal("optional data displaced or changed essential overview")
	}
	if !result.WriteBlocked || result.Pending.RequestID != "held" || result.ScaleSetInventory != nil {
		t.Fatal("optional failure misrepresented")
	}
	view.ScaleSetInventory = &garmapi.ScaleSetDiscovery{ScaleSets: []garmapi.DiscoveredScaleSet{}}
	if withBoundedScaleSetInventory(view).ScaleSetInventory == nil {
		t.Fatal("small complete empty observation omitted")
	}
}
