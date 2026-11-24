package brokerinput

import (
	"crypto/sha256"
	"slices"
	"testing"
)

const stockApplyFixtureInterface = "fixture_verified_receipts"

func enableStockApplyFixtureInterface(t *testing.T) {
	t.Helper()
	supportedStockApplyInterfaces[stockApplyFixtureInterface] = true
	t.Cleanup(func() { delete(supportedStockApplyInterfaces, stockApplyFixtureInterface) })
}

func stockApplyFixtureClaim() StockApplyCapabilityClaim {
	return StockApplyCapabilityClaim{
		Interface:         stockApplyFixtureInterface,
		ExternalOwnership: sha256.Sum256([]byte("verified sole ownership evidence")),
		CompletionProof:   sha256.Sum256([]byte("durable receipt contract evidence")),
		LateWriterFence:   sha256.Sum256([]byte("late writer fence evidence")),
	}
}

func refusalCodes(refusals []StockApplyRefusal) []StockApplyRefusalCode {
	out := make([]StockApplyRefusalCode, 0, len(refusals))
	for _, refusal := range refusals {
		out = append(out, refusal.Code)
	}
	return out
}

func TestStockApplyCapabilityStockInterfaceRefusedEvenWithEvidence(t *testing.T) {
	bare := AssessStockApplyCapability(StockApplyCapabilityClaim{Interface: StockApplyStockInterface})
	if bare.Supported {
		t.Fatal("bare stock claim supported")
	}
	codes := refusalCodes(bare.Refusals)
	if len(codes) == 0 || codes[0] != StockApplyRefusalStockObserveOnly {
		t.Fatal("stock refusal must name the observe-only interface first", codes)
	}
	for _, want := range []StockApplyRefusalCode{StockApplyRefusalExternalOwnershipUnproven, StockApplyRefusalCompletionProofUnavailable, StockApplyRefusalLateWriterFenceUnproven} {
		if !slices.Contains(codes, want) {
			t.Fatal("bare stock refusal must still name every missing proof", want, codes)
		}
	}
	// Populated evidence cannot make an ordinary stock PUT+GET conditional or
	// authoritative: the interface refusal stands alone.
	full := stockApplyFixtureClaim()
	full.Interface = StockApplyStockInterface
	verdict := AssessStockApplyCapability(full)
	if verdict.Supported || len(verdict.Refusals) != 1 || verdict.Refusals[0].Code != StockApplyRefusalStockObserveOnly {
		t.Fatal("evidence-populated stock claim", verdict)
	}
}

func TestStockApplyCapabilityUnknownAndEmptyInterfacesFailClosed(t *testing.T) {
	for _, claim := range []StockApplyCapabilityClaim{{}, {Interface: "future_unqualified_api"}} {
		verdict := AssessStockApplyCapability(claim)
		if verdict.Supported || len(verdict.Refusals) == 0 || verdict.Refusals[0].Code != StockApplyRefusalInterfaceUnsupported {
			t.Fatal("unknown interface admitted", claim.Interface, verdict)
		}
	}
}

func TestStockApplyCapabilityNamesEachMissingProof(t *testing.T) {
	enableStockApplyFixtureInterface(t)
	verdict := AssessStockApplyCapability(StockApplyCapabilityClaim{Interface: stockApplyFixtureInterface})
	codes := refusalCodes(verdict.Refusals)
	for _, want := range []StockApplyRefusalCode{StockApplyRefusalExternalOwnershipUnproven, StockApplyRefusalCompletionProofUnavailable, StockApplyRefusalLateWriterFenceUnproven} {
		if !slices.Contains(codes, want) {
			t.Fatal("missing refusal", want, codes)
		}
	}
	if verdict.Supported {
		t.Fatal("missing proofs supported")
	}
}

func TestStockApplyCapabilityFixtureQualifiesOnlyWithCompleteEvidence(t *testing.T) {
	enableStockApplyFixtureInterface(t)
	verdict := AssessStockApplyCapability(stockApplyFixtureClaim())
	if !verdict.Supported || len(verdict.Refusals) != 0 {
		t.Fatal("complete fixture evidence refused", verdict)
	}
	partial := stockApplyFixtureClaim()
	partial.LateWriterFence = [sha256.Size]byte{}
	verdict = AssessStockApplyCapability(partial)
	if verdict.Supported || !slices.Contains(refusalCodes(verdict.Refusals), StockApplyRefusalLateWriterFenceUnproven) {
		t.Fatal("partial evidence supported", verdict)
	}
}

func TestStockApplyAdapterStockGarmAlwaysUnavailable(t *testing.T) {
	verdict := AssessStockApplyAdapter(StockGarmAdapterClaim())
	if verdict.Availability != "unavailable" {
		t.Fatal("stock adapter available", verdict)
	}
	codes := refusalCodes(verdict.Refusals)
	if len(codes) == 0 || codes[0] != StockApplyRefusalStockObserveOnly {
		t.Fatal("stock adapter refusal must name the observe-only interface first", codes)
	}
	if !slices.Contains(codes, StockApplyRefusalPhysicalScope) {
		t.Fatal("stock adapter must refuse physical allocation/teardown scope", codes)
	}
	established := map[string]bool{}
	for _, refusal := range verdict.Refusals {
		if refusal.Code == StockApplyRefusalRequirementUnestablished {
			established[refusal.Detail] = true
		}
	}
	for _, requirement := range stockApplyAdapterRequirements {
		if !established[string(requirement)] {
			t.Fatal("stock adapter must report every unestablished requirement", requirement, verdict.Refusals)
		}
	}
}

func TestStockApplyAdapterFixtureQualifiesOnlyWithEveryRequirement(t *testing.T) {
	enableStockApplyFixtureInterface(t)
	digest := sha256.Sum256([]byte("qualified evidence"))
	claim := StockApplyAdapterClaim{Name: "fixture-adapter", Interface: stockApplyFixtureInterface, SettingsOnly: true,
		Establishes: map[StockApplyAdapterRequirement][sha256.Size]byte{
			StockApplyExclusiveChangeBoundary:  digest,
			StockApplyAuthoritativeOutcome:     digest,
			StockApplyLateWriterRacePrevention: digest,
			StockApplyDriftHandling:            digest,
			StockApplySafeRollback:             digest,
		}}
	if verdict := AssessStockApplyAdapter(claim); verdict.Availability != "available" || len(verdict.Refusals) != 0 {
		t.Fatal("complete fixture adapter unavailable", verdict)
	}
	delete(claim.Establishes, StockApplySafeRollback)
	verdict := AssessStockApplyAdapter(claim)
	if verdict.Availability != "unavailable" {
		t.Fatal("adapter missing a requirement stayed available", verdict)
	}
	found := false
	for _, refusal := range verdict.Refusals {
		if refusal.Code == StockApplyRefusalRequirementUnestablished && refusal.Detail == string(StockApplySafeRollback) {
			found = true
		}
	}
	if !found {
		t.Fatal("missing requirement not named", verdict.Refusals)
	}
	claim.Establishes[StockApplySafeRollback] = digest
	claim.SettingsOnly = false
	if verdict := AssessStockApplyAdapter(claim); verdict.Availability != "unavailable" ||
		!slices.Contains(refusalCodes(verdict.Refusals), StockApplyRefusalPhysicalScope) {
		t.Fatal("physical scope adapter admitted", verdict)
	}
}
