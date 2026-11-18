package adminview

import (
	"context"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsettings"
	"testing"
)

func TestManagedSettingsAdapterRejectsEveryNonMaximumField(t *testing.T) {
	v := &managedSettingsView{writes: true, pools: map[string]bool{"pool": true}, client: &managedsettings.Client{}}
	n := uint64(2)
	yes := true
	tags := []string{"linux"}
	for _, u := range []garmapi.PoolUpdate{{}, {MaxRunners: &n, Enabled: &yes}, {MaxRunners: &n, Priority: &n}, {MaxRunners: &n, MinIdleRunners: &n}, {MaxRunners: &n, Tags: &tags}} {
		if _, e := v.UpdatePool(context.Background(), "id", "pool", "fp", u, "reason"); !errors.Is(e, garmapi.ErrManagementUnsafe) {
			t.Fatal(e)
		}
	}
	if _, e := v.ResolvePool(context.Background(), "id"); !errors.Is(e, garmapi.ErrManagementDisabled) {
		t.Fatal(e)
	}
	if _, e := v.UpdateRepositoryBalancer(context.Background(), "id", "repo", "fp", "pack", "reason"); !errors.Is(e, garmapi.ErrManagementDisabled) {
		t.Fatal(e)
	}
}
