package brokerinput

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"strings"
	"testing"
	"time"
)

func TestStockIdlePreviewExplicitAndBounded(t *testing.T) {
	store, source := &admission.SQLiteStore{}, &garmapi.DemandClient{}
	legacy, _ := NewAdvisoryReader(store, source)
	maximum, _ := NewAdvisoryReaderWithStockMaximumPreview(store, source)
	opted, err := NewAdvisoryReaderWithStockPreviews(store, source, false, true)
	if err != nil || legacy.reader.stockIdlePreview || maximum.reader.stockIdlePreview || !opted.reader.stockIdlePreview || opted.reader.stockMaximumPreview {
		t.Fatal("opt-in changed")
	}
	raw, _ := json.Marshal(ReadPlan{})
	if strings.Contains(string(raw), "stock_idle_preview") {
		t.Fatal("default exposed")
	}
	state := admission.BrokerGrantState{}
	if _, err := sampleStockIdlePreview(state, nil, garmapi.DemandObservation{}, "", capacitybroker.Options{}); err == nil {
		t.Fatal("empty scope")
	}
	state.Config.Pools = make(map[string]admission.Pool)
	for _, id := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"} {
		state.Config.Pools[id] = admission.Pool{}
	}
	if _, err := sampleStockIdlePreview(state, nil, garmapi.DemandObservation{}, "", capacitybroker.Options{}); err == nil {
		t.Fatal("unbounded scope")
	}
}
func TestStockIdlePreviewInvalidHistoryAndOwnedCache(t *testing.T) {
	now := time.Now()
	h := stockIdleHistory{prior: map[string]stockIdleSample{"old": {}}}
	out := h.accept(nil, nil, now, now.Add(time.Minute))
	if out.State != "unavailable" || out.HostEvidence != "" || h.prior != nil || len(out.Proposals) != 0 {
		t.Fatal(out)
	}
	out = h.accept(map[string]stockIdleSample{"fake": {}}, nil, now, now.Add(time.Minute))
	if out.State != "unavailable" || out.HostEvidence != "" || h.prior != nil {
		t.Fatal("fabricated sample")
	}
	original := ReadPlan{StockIdlePreview: &StockIdlePreview{State: "proposed", Proposals: []StockIdleProposal{{PoolID: "pool", ObservedMinIdle: 1}}}}
	copy := cloneReadPlan(original)
	copy.StockIdlePreview.State = "bad"
	copy.StockIdlePreview.Proposals[0].ObservedMinIdle = 9
	if original.StockIdlePreview.State != "proposed" || original.StockIdlePreview.Proposals[0].ObservedMinIdle != 1 {
		t.Fatal("cache aliased")
	}
}
