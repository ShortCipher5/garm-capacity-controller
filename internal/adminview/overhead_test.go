package adminview

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestOverviewRetainedOverheadUsesSameSnapshot(t *testing.T) {
	_, store, _ := fixture(t)
	state, e := store.ReadSnapshot(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	state.Allocated.DomainOverhead = admission.Resources{CPU: 100, RAM: 200, Disk: 300}
	state.Allocated.ArtifactOverhead = admission.Resources{Disk: 400}
	state.Allocated.Global = admission.Resources{CPU: 1100, RAM: 1200, Disk: 1700, Runners: 1}
	before := state.Allocated
	out, e := overview(state, false, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if out.NonPoolOverhead.Domain != before.DomainOverhead || out.NonPoolOverhead.Artifact != before.ArtifactOverhead || out.Global.Allocated != before.Global || !reflect.DeepEqual(state.Allocated, before) {
		t.Fatal("overhead omitted, double counted or mutated")
	}
	raw, e := json.Marshal(out.NonPoolOverhead)
	if e != nil || len(raw) > 512 {
		t.Fatal("unexpected detailed proof exposure", e)
	}
}
func TestOverheadProjectionRejectsInvalidSubsets(t *testing.T) {
	for _, a := range []admission.Allocations{{DomainOverhead: admission.Resources{Runners: 1}, Global: admission.Resources{Runners: 1}}, {ArtifactOverhead: admission.Resources{CPU: 1}, Global: admission.Resources{CPU: 1}}, {DomainOverhead: admission.Resources{Disk: math.MaxInt64}, ArtifactOverhead: admission.Resources{Disk: 1}, Global: admission.Resources{Disk: math.MaxInt64}}, {ArtifactOverhead: admission.Resources{Disk: -1}}} {
		if _, e := overheadTotals(a); e == nil {
			t.Fatal("invalid overhead accepted")
		}
	}
}
