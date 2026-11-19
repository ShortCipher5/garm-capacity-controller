package brokerinput

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

func TestStockMaximumPreviewDisabledByDefault(t *testing.T) {
	store, source := &admission.SQLiteStore{}, &garmapi.DemandClient{}
	base, err := NewAdvisoryReader(store, source)
	if err != nil || base.reader.stockMaximumPreview || base.reader.scalingHistory.prior.source != "" {
		t.Fatal(base, err)
	}
	opted, err := NewAdvisoryReaderWithStockMaximumPreview(store, source)
	if err != nil || !opted.reader.stockMaximumPreview || opted.reader.source != source || opted.reader.store != store {
		t.Fatal(opted, err)
	}
	if payload, err := json.Marshal(ReadPlan{}); err != nil || strings.Contains(string(payload), "stock_maximum_preview") {
		t.Fatal(string(payload), err)
	}
}

func TestStockMaximumPreviewHistoryFailsClosed(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	sample := func(start time.Time, scope byte) scalingPreviewSample {
		return scalingPreviewSample{
			scope: [32]byte{scope},
			StockScalingSample: StockScalingSample{
				source: "scoped", revision: 1, sequence: 4, budget: 2,
				start: start, finish: start, expires: start.Add(45 * time.Second),
				pools:    map[string]scalingPool{"p": {repo: "r", enabled: true, current: 1, target: 2}},
				repoCaps: map[string]int64{"r": 2}, order: []string{"p"},
			},
		}
	}
	var history scalingPreviewHistory
	collect := func(offset time.Duration, scope byte, err error) *StockMaximumPreview {
		current := sample(now.Add(offset), scope)
		return history.accept(current, err, current.finish, current.finish.Add(time.Minute))
	}
	if first := collect(0, 1, nil); first.State != "no_confirmed_proposals" || first.Reason != "awaiting_matching_sample" || len(first.Proposals) != 0 {
		t.Fatal(first)
	}
	out := collect(20*time.Second, 1, nil)
	if out.State != "proposed" || !out.ExpiresAt.Equal(now.Add(45*time.Second)) || !reflect.DeepEqual(out.Proposals, []StockMaximumProposal{{"p", "r", 1, 2}}) {
		t.Fatal(out)
	}
	failed := collect(25*time.Second, 1, errors.New("private input detail"))
	if failed.State != "unavailable" || failed.Reason != "evidence_unavailable" || len(failed.Proposals) != 0 || history.prior.source != "" {
		t.Fatal(failed)
	}
	if afterFailure := collect(40*time.Second, 1, nil); len(afterFailure.Proposals) != 0 {
		t.Fatal("retained history across failure")
	}
	if changed := collect(60*time.Second, 2, nil); len(changed.Proposals) != 0 || changed.Reason != "awaiting_matching_sample" {
		t.Fatal("retained history across scope change")
	}
	if repeated := collect(60*time.Second, 2, nil); len(repeated.Proposals) != 0 || repeated.Reason != "no_confirmed_change" {
		t.Fatal("reused same sample")
	}
	if stale := collect(120*time.Second, 2, nil); len(stale.Proposals) != 0 {
		t.Fatal("used expired history")
	}
	raw, err := json.Marshal(out)
	if err != nil || string(raw) != `{"state":"proposed","expires_at":"2023-11-14T22:14:05Z","proposals":[{"pool_id":"p","repository_id":"r","observed":1,"suggested":2}]}` {
		t.Fatal(string(raw), err)
	}
}

func TestStockMaximumPreviewPressureOnlyVetoesAndExpires(t *testing.T) {
	snapshot, _, _, opt := fixture()
	at := opt.Now.Add(-pressureAge + time.Second)
	base := PressureStatus{Host: "host", State: "partial_sample_not_headroom", SampledAt: at}
	if expiry := stockMaximumPressureExpiry(snapshot, []PressureStatus{base}, opt.Now); !expiry.Equal(opt.Now.Add(time.Second)) {
		t.Fatal(expiry)
	}
	for _, status := range []PressureStatus{
		{Host: "host", State: "high_partial_pressure_suppressed", SampledAt: at},
		{Host: "host", State: "missing_or_stale_not_headroom"},
		{Host: "host", State: "window_overflow_suppressed", SampledAt: at},
		{Host: "foreign", State: base.State, SampledAt: at},
		{Host: "host", State: base.State, SampledAt: opt.Now.Add(-pressureAge)},
		{Host: "host", State: base.State, SampledAt: opt.Now.Add(time.Second)},
	} {
		if expiry := stockMaximumPressureExpiry(snapshot, []PressureStatus{status}, opt.Now); !expiry.IsZero() {
			t.Fatal("accepted uncertain pressure", status)
		}
	}
	if !stockMaximumPressureExpiry(snapshot, nil, opt.Now).IsZero() {
		t.Fatal("accepted incomplete pressure")
	}
}

func TestStockMaximumPreviewCacheDoesNotExposeStaleOrSharedRows(t *testing.T) {
	now := time.Now()
	p := &ReadPlan{ExpiresAt: now.Add(time.Minute), StockMaximumPreview: &StockMaximumPreview{
		State: "proposed", ExpiresAt: now.Add(30 * time.Second), Proposals: []StockMaximumProposal{{"p", "r", 1, 2}},
	}}
	s := &AdvisoryLoop{plan: p}
	fresh, err := s.ReadPlan(context.Background())
	if err != nil || fresh.StockMaximumPreview == nil {
		t.Fatal(fresh, err)
	}
	fresh.StockMaximumPreview.Proposals[0].Suggested = 100
	if p.StockMaximumPreview.Proposals[0].Suggested != 2 {
		t.Fatal("preview mutates cache")
	}
	p.StockMaximumPreview.ExpiresAt = now.Add(-time.Second)
	stale, err := s.ReadPlan(context.Background())
	if err != nil || stale.StockMaximumPreview != nil || !stale.ExpiresAt.Equal(p.ExpiresAt) {
		t.Fatal("stale preview escaped or invalidated ordinary plan", stale, err)
	}
}

func TestStockMaximumPreviewScopeDetectsEffectiveConfigurationChanges(t *testing.T) {
	state, _, _ := ledgerFixture()
	snapshot, _, _, _ := fixture()
	base, err := scalePreviewScope(state, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	// Observation refresh by itself is not a policy change.
	snapshot.ObservedAt = snapshot.ObservedAt.Add(time.Second)
	state.Sequence++
	same, err := scalePreviewScope(state, snapshot)
	if same != base || err != nil {
		t.Fatal("refresh invalidated same scope", err)
	}
	host := snapshot.Hosts["host"]
	host.AcceptNew = false
	snapshot.Hosts["host"] = host
	drift, err := scalePreviewScope(state, snapshot)
	if drift == base || err != nil {
		t.Fatal("ignored host eligibility change", err)
	}
}

func TestStockMaximumPreviewBoundedReasons(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		err        error
		incomplete bool
		want       string
	}{
		{errStockPreviewPressure, false, "pressure_evidence_unavailable"},
		{errors.New("private detail"), false, "evidence_unavailable"},
		{nil, true, "incomplete_evidence"},
	} {
		var history scalingPreviewHistory
		result := history.accept(scalingPreviewSample{StockScalingSample: StockScalingSample{source: "scoped", expires: now.Add(time.Minute), incomplete: tc.incomplete}}, tc.err, now, now.Add(time.Minute))
		if result.Reason != tc.want || result.State != "unavailable" || len(result.Proposals) != 0 {
			t.Fatal(result)
		}
		raw, _ := json.Marshal(result)
		if strings.Contains(string(raw), "private detail") {
			t.Fatal(string(raw))
		}
	}
	if !errors.Is(errStockPreviewPressure, capacitybroker.ErrSnapshot) {
		t.Fatal("lost snapshot classification")
	}
}
