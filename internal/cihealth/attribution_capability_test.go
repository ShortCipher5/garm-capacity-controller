package cihealth

import "testing"

func TestDocumentedMatrixProvesOnlyPublishedSourceFacts(t *testing.T) {
	m := DocumentedMatrix()
	for fact, supported := range map[AttributionFact]bool{
		FactExactJobIdentity:           true,
		FactPositiveRunnerRegistration: true,
		FactExactStockRow:              true,
		FactPositiveStockRowAgent:      true,
		FactIncusIncarnation:           true,
		FactProcessEpoch:               true,
		FactCPUCounters:                true,
		FactRAMCounters:                true,
		// The whole point of the matrix: no documented read proves the
		// provider-origin join or per-instance disk counters.
		FactProviderOriginJoin: false,
		FactDiskCounters:       false,
	} {
		if got := m.Supports(fact); got != supported {
			t.Fatalf("matrix support for %q = %t, want %t", fact, got, supported)
		}
	}
}

func TestDocumentedMatrixPerSourceCapabilities(t *testing.T) {
	m := DocumentedMatrix()
	cases := []struct {
		class   AttributionEvidenceClass
		proves  []AttributionFact
		carries []AttributionFact
		never   []AttributionFact
	}{
		{
			class:  EvidenceGitHubAttemptJobs,
			proves: []AttributionFact{FactExactJobIdentity, FactPositiveRunnerRegistration},
			never: []AttributionFact{
				FactExactStockRow, FactPositiveStockRowAgent, FactStockProviderName, FactIncusIncarnation,
				FactProcessEpoch, FactCPUCounters, FactRAMCounters, FactProviderOriginJoin, FactDiskCounters,
			},
		},
		{
			class:   EvidenceGARMRunningRegistration,
			proves:  []AttributionFact{FactExactStockRow, FactPositiveStockRowAgent},
			carries: []AttributionFact{FactStockProviderName},
			never: []AttributionFact{
				FactExactJobIdentity, FactPositiveRunnerRegistration, FactIncusIncarnation, FactProcessEpoch,
				FactCPUCounters, FactRAMCounters, FactProviderOriginJoin, FactDiskCounters,
			},
		},
		{
			class:  EvidenceIncusRuntimeHealth,
			proves: []AttributionFact{FactIncusIncarnation, FactProcessEpoch, FactCPUCounters, FactRAMCounters},
			never: []AttributionFact{
				FactExactJobIdentity, FactPositiveRunnerRegistration, FactExactStockRow, FactPositiveStockRowAgent,
				FactStockProviderName, FactProviderOriginJoin, FactDiskCounters,
			},
		},
	}
	for _, tc := range cases {
		for _, fact := range tc.proves {
			if !m.ClassProves(tc.class, fact) {
				t.Fatalf("%s must positively prove %q", tc.class, fact)
			}
		}
		for _, fact := range tc.carries {
			if m.ClassProves(tc.class, fact) {
				t.Fatalf("%s merely carries unauthenticated %q and must not prove it", tc.class, fact)
			}
		}
		for _, fact := range tc.never {
			if m.ClassProves(tc.class, fact) {
				t.Fatalf("%s must never prove %q", tc.class, fact)
			}
		}
	}
	// Every documented source explicitly records that it cannot supply the
	// provider-origin join; that gap is what keeps live attribution closed.
	for _, source := range m.Sources {
		found := false
		for _, fact := range source.NeverProves {
			if fact == FactProviderOriginJoin {
				found = true
			}
		}
		if !found {
			t.Fatalf("source %s must document the missing provider-origin join", source.Class)
		}
	}
	if m.ClassProves("not_a_source", FactCPUCounters) {
		t.Fatal("unknown source must not prove anything")
	}
}

// fixtureJoinMatrix is a fixture-only matrix standing in for a future
// qualified producer of original provider allocation evidence. It exists so
// the verified paths can be exercised offline; DocumentedMatrix remains the
// production contract and keeps the join unsupported.
func fixtureJoinMatrix() AttributionCapabilityMatrix {
	m := DocumentedMatrix()
	m.Sources = append(m.Sources, AttributionCapability{
		Class:  "fixture_verified_origin",
		Proves: []AttributionFact{FactProviderOriginJoin},
	})
	return m
}
