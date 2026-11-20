package brokerinput

import (
	"math"
	"reflect"
	"strconv"
	"testing"
	"time"
)

var stockDiskTestNow = time.Unix(300, 0)

func stockDiskFixture() stockMaximumDiskInputs {
	sample := stockDiskHeadroomSample{
		at:    stockDiskTestNow.Add(-5 * time.Second),
		total: stockDiskAmount{1001, 100}, available: stockDiskAmount{401, 25},
	}
	return stockMaximumDiskInputs{
		sample:         &sample,
		reserve:        stockDiskReserve{absolute: stockDiskAmount{90, 10}, byteBasisPoints: 1000, inodeBasisPoints: 500},
		unspentPending: &stockDiskPendingEstimate{sample: sample, unspent: stockDiskAmount{100, 5}, expiresAt: stockDiskTestNow.Add(10 * time.Second)},
		maxAge:         30 * time.Second,
	}
}

func stockDiskChange(pool string, before, after int64, cost *stockDiskAmount) stockMaximumDiskCandidate {
	return stockMaximumDiskCandidate{
		proposal:    StockMaximumProposal{PoolID: pool, RepositoryID: "fixture-repo", Observed: before, Suggested: after},
		incremental: cost,
	}
}

func TestStockMaximumDiskBrakeUsesBothReservesAndSharedHeadroom(t *testing.T) {
	in := stockDiskFixture()
	// Available after max(abs,ceil(fraction×total)) and unspent estimates:
	// bytes = 401 - 101 - 100 = 200; inodes = 25 - 10 - 5 = 10.
	rows := []stockMaximumDiskCandidate{
		stockDiskChange("inode", 2, 3, &stockDiskAmount{1, 11}),
		stockDiskChange("cut", 4, 3, nil),
		stockDiskChange("a", 2, 3, &stockDiskAmount{150, 5}),
		stockDiskChange("b", 2, 3, &stockDiskAmount{51, 1}),
		stockDiskChange("c", 2, 3, &stockDiskAmount{50, 5}),
		stockDiskChange("d", 2, 3, &stockDiskAmount{1, 1}),
	}
	want := []StockMaximumProposal{rows[1].proposal, rows[2].proposal, rows[4].proposal}
	vetoes := []stockMaximumDiskVeto{
		{"inode", stockDiskInodesReserve}, {"b", stockDiskBytesReserve}, {"d", stockDiskBytesReserve},
	}
	got := vetoStockMaximumDiskGrowth(rows, &in, stockDiskTestNow)
	if !reflect.DeepEqual(got.proposals, want) || !reflect.DeepEqual(got.vetoes, vetoes) ||
		got.growthExpiry != in.unspentPending.expiresAt {
		t.Fatalf("unexpected growth brake result: %+v", got)
	}
	// With the same available measurements and known zero pending, 300 bytes
	// and 15 inodes fit, even though actual materialized usage is nonzero. It
	// is already subtracted from measured free space, never charged again.
	in.unspentPending.unspent = stockDiskAmount{}
	one := stockDiskChange("a", 2, 3, &stockDiskAmount{300, 15})
	got = vetoStockMaximumDiskGrowth([]stockMaximumDiskCandidate{one}, &in, stockDiskTestNow)
	if !reflect.DeepEqual(got.proposals, []StockMaximumProposal{one.proposal}) || len(got.vetoes) != 0 {
		t.Fatalf("measured free space was counted twice: %+v", got)
	}
}

func TestStockMaximumDiskBrakeUnknownKeepsReductions(t *testing.T) {
	value := int64(0)
	rows := []stockMaximumDiskCandidate{
		stockDiskChange("a", 1, 2, &stockDiskAmount{1, 1}),
		stockDiskChange("cut", 2, 1, nil),
	}
	rows[1].proposal.ObservedMinIdle = &value
	for _, tc := range []struct {
		label string
		edit  func(*stockMaximumDiskInputs)
		want  stockMaximumDiskReason
	}{
		{"missing", func(in *stockMaximumDiskInputs) { in.sample = nil }, stockDiskUnavailable},
		{"freshness limit missing", func(in *stockMaximumDiskInputs) { in.maxAge = 0 }, stockDiskUnavailable},
		{"freshness limit excessive", func(in *stockMaximumDiskInputs) { in.maxAge = pressureAge + time.Second }, stockDiskUnavailable},
		{"at expiry", func(in *stockMaximumDiskInputs) { in.sample.at = stockDiskTestNow.Add(-in.maxAge) }, stockDiskStale},
		{"future", func(in *stockMaximumDiskInputs) { in.sample.at = stockDiskTestNow.Add(time.Second) }, stockDiskUnavailable},
		{"unknown total inodes", func(in *stockMaximumDiskInputs) { in.sample.total.inodes = 0 }, stockDiskUnavailable},
		{"impossible values", func(in *stockMaximumDiskInputs) { in.sample.available.bytes = in.sample.total.bytes + 1 }, stockDiskUnavailable},
		{"invalid fraction", func(in *stockMaximumDiskInputs) { in.reserve.byteBasisPoints = 10001 }, stockDiskUnavailable},
		{"missing reserve", func(in *stockMaximumDiskInputs) {
			in.reserve.absolute.inodes, in.reserve.inodeBasisPoints = 0, 0
		}, stockDiskUnavailable},
		{"unknown pending", func(in *stockMaximumDiskInputs) { in.unspentPending = nil }, stockDiskPendingUnknown},
		{"expired pending", func(in *stockMaximumDiskInputs) { in.unspentPending.expiresAt = stockDiskTestNow }, stockDiskPendingUnknown},
		{"no available bytes", func(in *stockMaximumDiskInputs) {
			in.sample.available.bytes = 0
			in.unspentPending.sample = *in.sample
		}, stockDiskBytesReserve},
		{"unspent bytes too high", func(in *stockMaximumDiskInputs) { in.unspentPending.unspent.bytes = math.MaxUint64 }, stockDiskBytesReserve},
		{"no available inodes", func(in *stockMaximumDiskInputs) {
			in.sample.available.inodes = 0
			in.unspentPending.sample = *in.sample
		}, stockDiskInodesReserve},
	} {
		t.Run(tc.label, func(t *testing.T) {
			in := stockDiskFixture()
			tc.edit(&in)
			got := vetoStockMaximumDiskGrowth(rows, &in, stockDiskTestNow)
			if !reflect.DeepEqual(got.proposals, []StockMaximumProposal{rows[1].proposal}) ||
				!reflect.DeepEqual(got.vetoes, []stockMaximumDiskVeto{{"a", tc.want}}) || !got.growthExpiry.IsZero() {
				t.Fatalf("expected exact reduction only: %+v", got)
			}
		})
	}
}

func TestStockMaximumDiskBrakeRejectsAmbiguousGrowth(t *testing.T) {
	in := stockDiskFixture()
	rows := []stockMaximumDiskCandidate{
		stockDiskChange("ambiguous", 2, 3, &stockDiskAmount{1, 1}),
		stockDiskChange("missing", 2, 3, nil),
		stockDiskChange("unknown inodes", 2, 3, &stockDiskAmount{1, 0}),
		stockDiskChange("large step", 2, 4, &stockDiskAmount{1, 1}),
		stockDiskChange("previous zero", 0, 1, &stockDiskAmount{1, 1}),
		stockDiskChange("ambiguous", 2, 1, nil),
		stockDiskChange("next", 1, 2, &stockDiskAmount{200, 10}),
	}
	got := vetoStockMaximumDiskGrowth(rows, &in, stockDiskTestNow)
	if !reflect.DeepEqual(got.proposals, []StockMaximumProposal{rows[5].proposal, rows[6].proposal}) ||
		len(got.vetoes) != 5 || got.growthExpiry.IsZero() {
		t.Fatalf("unexpected growth brake result: %+v", got)
	}
	for _, veto := range got.vetoes {
		if veto.reason != stockDiskGrowthUnknown {
			t.Fatalf("unexpected veto: %+v", veto)
		}
	}
}

func TestStockMaximumDiskBrakeFractionDoesNotOverflow(t *testing.T) {
	for _, tc := range []struct{ total, bp, want uint64 }{
		{math.MaxUint64, 0, 0},
		{math.MaxUint64, 10000, math.MaxUint64},
		{math.MaxUint64, 5000, 1 << 63},
		{1001, 1000, 101},
		{1, 1, 1},
	} {
		if got := stockDiskFractionCeil(tc.total, tc.bp); got != tc.want {
			t.Fatalf("ceil(%d × %d) = %d, want %d", tc.total, tc.bp, got, tc.want)
		}
	}
}

func TestStockMaximumDiskBrakeBoundedBatch(t *testing.T) {
	in := stockDiskFixture()
	rows := make([]stockMaximumDiskCandidate, 1000)
	rows[0] = stockDiskChange("only-positive", 2, 3, &stockDiskAmount{1, 1})
	for i := 1; i < len(rows); i++ {
		rows[i] = stockDiskChange("cut-"+strconv.Itoa(i), 2, 1, nil)
	}
	atLimit := vetoStockMaximumDiskGrowth(rows, &in, stockDiskTestNow)
	if atLimit.invalidBatch || len(atLimit.proposals) != len(rows) || len(atLimit.vetoes) != 0 || atLimit.growthExpiry.IsZero() {
		t.Fatal("exact input limit must preserve valid growth and reductions")
	}
	overLimit := vetoStockMaximumDiskGrowth(append(rows, stockDiskChange("extra-cut", 2, 1, nil)), &in, stockDiskTestNow)
	if !overLimit.invalidBatch || overLimit.proposals != nil || overLimit.vetoes != nil || !overLimit.growthExpiry.IsZero() {
		t.Fatal("oversize input must be structurally rejected without any partial result")
	}
}
