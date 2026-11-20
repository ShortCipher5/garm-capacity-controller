package brokerinput

import (
	"math"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func stockDiskPendingFixture() (*stockDiskHeadroomSample, *stockDiskPendingCapture, map[string]stockDiskAmount) {
	sample := stockDiskFixture().sample
	capture := &stockDiskPendingCapture{
		sample: *sample, expiresAt: stockDiskTestNow.Add(10 * time.Second),
		scope: map[string]string{"work-1": "small", "work-2": "heavy", "cleanup-1": "small"},
		rows: []stockDiskWorkFootprint{
			{"work-1", "small", stockDiskWorkMayAllocate, &stockDiskAmount{}}, // Not yet materialized.
			{"work-2", "heavy", stockDiskWorkMayAllocate, &stockDiskAmount{150, 15}},
			{"cleanup-1", "small", stockDiskWorkQuiescentRetained, &stockDiskAmount{70, 7}},
		},
	}
	return sample, capture, map[string]stockDiskAmount{"small": {100, 10}, "heavy": {200, 20}}
}

func TestStockDiskPendingEstimateAccountsOnlyUnspent(t *testing.T) {
	sample, capture, planned := stockDiskPendingFixture()
	want := &stockDiskPendingEstimate{sample: *sample, unspent: stockDiskAmount{150, 15}, expiresAt: capture.expiresAt}
	if got := estimateStockDiskPending(sample, capture, planned, stockDiskTestNow); !reflect.DeepEqual(got, want) {
		t.Fatalf("unspent result = %+v, want %+v", got, want)
	}
	capture.rows[0], capture.rows[2] = capture.rows[2], capture.rows[0]
	estimate := estimateStockDiskPending(sample, capture, planned, stockDiskTestNow)
	if !reflect.DeepEqual(estimate, want) {
		t.Fatal("estimate depends on row ordering")
	}
	in := stockDiskFixture()
	in.sample, in.unspentPending = sample, estimate
	rows := []stockMaximumDiskCandidate{
		stockDiskChange("inode-pressure", 3, 4, &stockDiskAmount{1, 1}),
		stockDiskChange("unchanged-cut", 4, 3, nil),
	}
	// 25 measured available inodes minus 10 reserve minus 15 unspent = 0.
	// The quiescent retained cleanup row and the cut grant no extra inodes.
	got := vetoStockMaximumDiskGrowth(rows, &in, stockDiskTestNow)
	if !reflect.DeepEqual(got.proposals, []StockMaximumProposal{rows[1].proposal}) ||
		!reflect.DeepEqual(got.vetoes, []stockMaximumDiskVeto{{"inode-pressure", stockDiskInodesReserve}}) {
		t.Fatalf("retained state or a cut credited space: %+v", got)
	}
}

func TestStockDiskPendingEstimateUnknownIsNotZero(t *testing.T) {
	for _, tc := range []struct {
		label string
		edit  func(*stockDiskHeadroomSample, *stockDiskPendingCapture, map[string]stockDiskAmount)
	}{
		{"absent scope", func(_ *stockDiskHeadroomSample, c *stockDiskPendingCapture, _ map[string]stockDiskAmount) {
			c.scope = nil
		}},
		{"missing work", func(_ *stockDiskHeadroomSample, c *stockDiskPendingCapture, _ map[string]stockDiskAmount) {
			c.rows = c.rows[:2]
		}},
		{"duplicate work", func(_ *stockDiskHeadroomSample, c *stockDiskPendingCapture, _ map[string]stockDiskAmount) {
			c.rows[1] = c.rows[0]
		}},
		{"out of scope", func(_ *stockDiskHeadroomSample, c *stockDiskPendingCapture, _ map[string]stockDiskAmount) {
			c.rows[0].id = "other"
		}},
		{"wrong profile", func(_ *stockDiskHeadroomSample, c *stockDiskPendingCapture, _ map[string]stockDiskAmount) {
			c.rows[0].profile = "heavy"
		}},
		{"unmapped profile", func(_ *stockDiskHeadroomSample, _ *stockDiskPendingCapture, p map[string]stockDiskAmount) {
			delete(p, "heavy")
		}},
		{"unknown cleanup phase", func(_ *stockDiskHeadroomSample, c *stockDiskPendingCapture, _ map[string]stockDiskAmount) {
			c.rows[2].phase = stockDiskWorkUnknown
		}},
		{"unknown cleanup footprint", func(_ *stockDiskHeadroomSample, c *stockDiskPendingCapture, _ map[string]stockDiskAmount) {
			c.rows[2].accounted = nil
		}},
		{"unknown pending footprint", func(_ *stockDiskHeadroomSample, c *stockDiskPendingCapture, _ map[string]stockDiskAmount) {
			c.rows[0].accounted = nil
		}},
		{"bound exceeded", func(_ *stockDiskHeadroomSample, c *stockDiskPendingCapture, _ map[string]stockDiskAmount) {
			c.rows[1].accounted.inodes = 21
		}},
		{"sample state mismatched", func(s *stockDiskHeadroomSample, _ *stockDiskPendingCapture, _ map[string]stockDiskAmount) {
			s.available.bytes--
		}},
		{"sample time mismatched", func(s *stockDiskHeadroomSample, _ *stockDiskPendingCapture, _ map[string]stockDiskAmount) {
			s.at = s.at.Add(-time.Second)
		}},
		{"over-attributed physical usage", func(s *stockDiskHeadroomSample, c *stockDiskPendingCapture, _ map[string]stockDiskAmount) {
			s.available.bytes = s.total.bytes - 200 // 220 bytes claimed to have materialized.
			c.sample = *s
		}},
		{"expired", func(_ *stockDiskHeadroomSample, c *stockDiskPendingCapture, _ map[string]stockDiskAmount) {
			c.expiresAt = stockDiskTestNow
		}},
		{"excessive freshness claim", func(s *stockDiskHeadroomSample, c *stockDiskPendingCapture, _ map[string]stockDiskAmount) {
			c.expiresAt = s.at.Add(pressureAge + 1)
		}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			sample, capture, planned := stockDiskPendingFixture()
			tc.edit(sample, capture, planned)
			if got := estimateStockDiskPending(sample, capture, planned, stockDiskTestNow); got != nil {
				t.Fatal("incomplete or inconsistent evidence became a usable estimate")
			}
		})
	}
	sample, capture, _ := stockDiskPendingFixture()
	capture.scope, capture.rows = map[string]string{}, nil
	if got := estimateStockDiskPending(sample, capture, nil, stockDiskTestNow); got == nil || got.unspent != (stockDiskAmount{}) {
		t.Fatal("complete explicit empty scope should model known zero")
	}
}

func TestStockDiskPendingEstimateBoundsAndOverflow(t *testing.T) {
	sample, capture, planned := stockDiskPendingFixture()
	planned["small"], planned["heavy"] = stockDiskAmount{math.MaxUint64, 20}, stockDiskAmount{math.MaxUint64, 20}
	if got := estimateStockDiskPending(sample, capture, planned, stockDiskTestNow); got != nil {
		t.Fatal("accepted estimate overflow")
	}
	capture.scope = make(map[string]string)
	capture.rows = nil
	planned = map[string]stockDiskAmount{"small": {1, 1}}
	for i := 0; i < 1000; i++ {
		id := "work-" + strconv.Itoa(i)
		capture.scope[id] = "small"
		capture.rows = append(capture.rows, stockDiskWorkFootprint{id, "small", stockDiskWorkMayAllocate, &stockDiskAmount{}})
	}
	if got := estimateStockDiskPending(sample, capture, planned, stockDiskTestNow); got == nil || got.unspent != (stockDiskAmount{1000, 1000}) {
		t.Fatal("exact row limit refused")
	}
	capture.scope["extra"] = "small"
	capture.rows = append(capture.rows, stockDiskWorkFootprint{"extra", "small", stockDiskWorkMayAllocate, &stockDiskAmount{}})
	if got := estimateStockDiskPending(sample, capture, planned, stockDiskTestNow); got != nil {
		t.Fatal("oversized scope accepted")
	}
}

func TestStockDiskPendingEstimateCannotRebind(t *testing.T) {
	rows := []stockMaximumDiskCandidate{
		stockDiskChange("growth", 2, 3, &stockDiskAmount{1, 1}),
		stockDiskChange("cut", 4, 3, nil),
	}
	for _, knownEmpty := range []bool{false, true} {
		in := stockDiskFixture()
		sample, capture, planned := stockDiskPendingFixture()
		sample.available.inodes++
		capture.sample = *sample
		in.sample = sample
		if knownEmpty {
			capture.scope, capture.rows, planned = map[string]string{}, nil, nil
		}
		estimate := estimateStockDiskPending(sample, capture, planned, stockDiskTestNow)
		if estimate == nil || (estimate.unspent == (stockDiskAmount{})) != knownEmpty {
			t.Fatal("invalid exact-sample fixture estimate")
		}
		in.unspentPending = estimate
		// The exact original captured values can be consumed, including an
		// explicit empty scope. A copy of the value retains its correspondence.
		original := vetoStockMaximumDiskGrowth(rows, &in, stockDiskTestNow)
		if len(original.proposals) != 2 || len(original.vetoes) != 0 {
			t.Fatal("did not accept exact sample")
		}
		for _, mutate := range []func(*stockDiskHeadroomSample){
			func(s *stockDiskHeadroomSample) { s.at = s.at.Add(time.Second) },
			func(s *stockDiskHeadroomSample) { s.total.bytes++ },
			func(s *stockDiskHeadroomSample) { s.total.inodes++ },
			func(s *stockDiskHeadroomSample) { s.available.bytes++ },
			func(s *stockDiskHeadroomSample) { s.available.inodes++ },
		} {
			rebound := *sample
			mutate(&rebound)
			in.sample = &rebound
			out := vetoStockMaximumDiskGrowth(rows, &in, stockDiskTestNow)
			if !reflect.DeepEqual(out.proposals, []StockMaximumProposal{rows[1].proposal}) ||
				!reflect.DeepEqual(out.vetoes, []stockMaximumDiskVeto{{"growth", stockDiskPendingUnknown}}) ||
				!out.growthExpiry.IsZero() {
				t.Fatal("different sample reused an estimate")
			}
		}
	}
}
