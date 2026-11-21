package hostmetrics

import (
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

func TestCollectorGuestCPUEpochChangesDoNotBridgeRates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*incusapi.UtilizationSample)
		valid  bool
	}{
		{"same count replacement", func(s *incusapi.UtilizationSample) {
			s.GuestCPU[0].Name = "replacement"
		}, true},
		{"same name new creation", func(s *incusapi.UtilizationSample) {
			s.GuestCPU[0].CreatedAt = s.GuestCPU[0].CreatedAt.Add(time.Nanosecond)
		}, true},
		{"same PID later start", func(s *incusapi.UtilizationSample) {
			s.GuestCPU[0].StartedAt = s.GuestCPU[0].StartedAt.Add(time.Nanosecond)
		}, true},
		{"same start different PID", func(s *incusapi.UtilizationSample) {
			s.GuestCPU[0].PID += 100
		}, true},
		{"one counter resets while total rises", func(s *incusapi.UtilizationSample) {
			s.GuestCPU[0].CPUUsedNanos = 1
			s.GuestCPU[1].CPUUsedNanos = s.CPUUsedMillis*int64(time.Millisecond) + 1
		}, true},
		{"sub-millisecond regression while total rises", func(s *incusapi.UtilizationSample) {
			s.GuestCPU[0].CPUUsedNanos = 10_000 * int64(time.Millisecond) // previous whole millis plus one ns
			s.GuestCPU[1].CPUUsedNanos = (s.CPUUsedMillis-10_000)*int64(time.Millisecond) + 1
		}, true},
		{"absence", func(s *incusapi.UtilizationSample) { s.GuestCPU = nil }, false},
		{"uncovered member", func(s *incusapi.UtilizationSample) { s.GuestCPU = s.GuestCPU[:1] }, false},
		{"duplicate member", func(s *incusapi.UtilizationSample) { s.GuestCPU[1].Name = s.GuestCPU[0].Name }, false},
		{"reused PID between members", func(s *incusapi.UtilizationSample) { s.GuestCPU[1].PID = s.GuestCPU[0].PID }, false},
		{"missing start", func(s *incusapi.UtilizationSample) { s.GuestCPU[0].StartedAt = time.Time{} }, false},
		{"missing creation", func(s *incusapi.UtilizationSample) { s.GuestCPU[0].CreatedAt = time.Time{} }, false},
		{"inverted epoch", func(s *incusapi.UtilizationSample) {
			s.GuestCPU[0].StartedAt = s.GuestCPU[0].CreatedAt.Add(-time.Nanosecond)
		}, false},
		{"missing PID", func(s *incusapi.UtilizationSample) { s.GuestCPU[0].PID = 0 }, false},
		{"ambiguous raw zero", func(s *incusapi.UtilizationSample) {
			s.GuestCPU[0].CPUUsedNanos = 0
			s.GuestCPU[1].CPUUsedNanos = s.CPUUsedMillis*int64(time.Millisecond) + 1
		}, false},
		{"inconsistent aggregate", func(s *incusapi.UtilizationSample) { s.CPUUsedMillis++ }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
			source, recorder := &fakeSource{}, &fakeRecorder{}
			c, err := New(map[string]Source{"fixture": source}, recorder, Options{})
			if err != nil {
				t.Fatal(err)
			}
			poll := func(sample incusapi.UtilizationSample) {
				source.sample = sample
				c.collect(t.Context(), at)
				at = at.Add(time.Minute)
			}
			poll(fixtureUsage(20_000, 100, 200, 2))
			changed := fixtureUsage(100_000, 100, 200, 2)
			tc.change(&changed)
			poll(changed)
			if got := recorder.recorded(); len(got) != 0 {
				t.Fatalf("broke continuity but published: %+v", got)
			}
			if !tc.valid {
				changed = fixtureUsage(100_000, 100, 200, 2)
				poll(changed)
				if got := recorder.recorded(); len(got) != 0 {
					t.Fatalf("bridged a missing or invalid baseline: %+v", got)
				}
			}
			// Reuse the producer's backing slice across calls. The held prior
			// baseline must not alias it. Order/time-zone presentation can vary.
			changed.GuestCPU[0].CPUUsedNanos += 25_000 * int64(time.Millisecond)
			changed.GuestCPU[1].CPUUsedNanos += 35_000 * int64(time.Millisecond)
			changed.GuestCPU[0].StartedAt = changed.GuestCPU[0].StartedAt.In(time.FixedZone("fixture-offset", 3600))
			changed.GuestCPU[0], changed.GuestCPU[1] = changed.GuestCPU[1], changed.GuestCPU[0]
			changed.CPUUsedMillis += 60_000
			changed.RAMUsedMiB, changed.DiskUsedMiB = 300, 400
			poll(changed)
			got := recorder.recorded()
			if len(got) != 1 || got[0].cpuUsedMillis != 1000 || got[0].ramUsedMiB != 300 ||
				got[0].diskUsedMiB != 400 || got[0].source != GuestStateSourceV2 || !got[0].at.Equal(at.Add(-time.Minute)) {
				t.Fatalf("did not resume with exact matching members and existing history schema: %+v", got)
			}
		})
	}
}

func TestCollectorDistinguishesKnownEmptyFromMissingGuestCPUCoverage(t *testing.T) {
	at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	source, recorder := &fakeSource{}, &fakeRecorder{}
	c, err := New(map[string]Source{"fixture": source}, recorder, Options{})
	if err != nil {
		t.Fatal(err)
	}
	empty := fixtureUsage(0, 0, 0, 0)
	missing := incusapi.UtilizationSample{}
	invalidEmpty := fixtureUsage(0, 1, 2, 0)
	cases := []incusapi.UtilizationSample{missing, missing, empty, missing, invalidEmpty, invalidEmpty, empty, empty}
	for i, sample := range cases {
		source.sample = sample
		c.collect(t.Context(), at.Add(time.Duration(i)*time.Minute))
		if i < len(cases)-1 && len(recorder.recorded()) != 0 {
			t.Fatalf("missing or unmatched sample became history at poll %d", i)
		}
	}
	got := recorder.recorded()
	if len(got) != 1 || got[0].cpuUsedMillis != 0 || got[0].ramUsedMiB != 0 || got[0].diskUsedMiB != 0 || got[0].source != GuestStateSourceV2 {
		t.Fatalf("did not preserve a genuinely known-empty interval: %+v", got)
	}
}
