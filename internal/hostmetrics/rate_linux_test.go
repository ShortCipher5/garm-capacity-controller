package hostmetrics

import (
	"context"
	"math"
	"math/big"
	"testing"
	"time"
)

func TestCPURateCannotWrapToLowPressure(t *testing.T) {
	for _, delta := range []int64{0, 1, 1500, math.MaxInt64 / 1000, math.MaxInt64/1000 + 1, math.MaxInt64/500 + 1, math.MaxInt64} {
		for _, elapsed := range []time.Duration{time.Millisecond, 1001 * time.Millisecond, time.Minute, time.Duration(math.MaxInt64)} {
			expected := new(big.Int).Mul(big.NewInt(delta), big.NewInt(1000))
			expected.Quo(expected, big.NewInt(int64(elapsed/time.Millisecond)))
			got, ok := cpuRate(delta, elapsed)
			if ok != expected.IsInt64() || (ok && got != expected.Int64()) {
				t.Fatalf("delta=%d elapsed=%v got=%d,%v expected=%s", delta, elapsed, got, ok, expected)
			}
		}
	}
	for _, elapsed := range []time.Duration{-time.Second, 0, time.Microsecond} {
		if _, ok := cpuRate(1, elapsed); ok {
			t.Fatal("invalid elapsed accepted")
		}
	}
	if _, ok := cpuRate(-1, time.Second); ok {
		t.Fatal("negative delta accepted")
	}
}

func TestCollectorRejectsUnrepresentableRateAndInvalidCounter(t *testing.T) {
	for _, value := range []int64{-1, math.MaxInt64} {
		source := &fakeSource{}
		recorder := &fakeRecorder{}
		c, err := New(map[string]Source{"synthetic": source}, recorder, Options{})
		if err != nil {
			t.Fatal(err)
		}
		at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		c.collectHost(context.Background(), "synthetic", source, at)
		source.sample.CPUUsedMillis = value
		c.collectHost(context.Background(), "synthetic", source, at.Add(time.Millisecond))
		if len(recorder.recorded()) != 0 {
			t.Fatal("invalid rate recorded as usage")
		}
		if !c.states["synthetic"].skipUntil.After(at.Add(time.Millisecond)) {
			t.Fatal("invalid sample did not back off")
		}
	}
}

func TestCollectorPreservesHighRateWithoutMultiplicationWrap(t *testing.T) {
	source := &fakeSource{}
	recorder := &fakeRecorder{}
	c, err := New(map[string]Source{"synthetic": source}, recorder, Options{})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.collectHost(context.Background(), "synthetic", source, at)
	source.sample.CPUUsedMillis = math.MaxInt64/500 + 1
	c.collectHost(context.Background(), "synthetic", source, at.Add(time.Second))
	got := recorder.recorded()
	if len(got) != 1 || got[0].cpuUsedMillis != source.sample.CPUUsedMillis {
		t.Fatal("high observation became low usage", got)
	}
}
