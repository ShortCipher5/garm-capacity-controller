package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
)

const sourceRequestID = "11111111-1111-4111-8111-111111111111"

func sourceFactsFixture() garmmanagement.DisabledPoolFacts {
	return garmmanagement.DisabledPoolFacts{Protocol: garmmanagement.DisabledPoolProtocol, ManagementIdentity: strings.Repeat("a", 64), JournalLineage: strings.Repeat("b", 64), RequestID: sourceRequestID, PoolID: "22222222-2222-4222-8222-222222222222", RepositoryID: "33333333-3333-4333-8333-333333333333", ProviderName: "capacity", ForgeEndpointName: "forge", Generation: 42, SpecFingerprint: strings.Repeat("c", 64), PoolFingerprint: strings.Repeat("d", 64), Image: "image", Flavor: "small", OSType: "linux", OSArch: "amd64", ExtraSpecsEmpty: true, MaxRunners: 2, ObservedAt: time.Unix(123, 0).UTC()}
}
func TestPoolRouteSourceCopiesTypedFactsWithoutGrantOrTimestampInference(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		t.Run(map[bool]string{false: "expected", true: "unsupported-must-not-normalize"}[unsupported], func(t *testing.T) {
			facts := sourceFactsFixture()
			if unsupported {
				facts.Protocol = "unsupported"
				facts.OSType = "other-os"
				facts.OSArch = "other-arch"
				facts.TemplateID = 7
				facts.ShellEnabled = true
				facts.ExtraSpecsEmpty = false
				facts.Enabled = true
				facts.MinIdleRunners = 5
				facts.MaxRunners = 3
			}
			type marker struct{}
			ctx := context.WithValue(context.Background(), marker{}, "source-context")
			calls := 0
			source := &poolRouteSource{confirmed: func(actual context.Context, id string) (garmmanagement.DisabledPoolFacts, error) {
				calls++
				if actual != ctx || id != sourceRequestID {
					t.Fatal("adapter changed request identity/context")
				}
				return facts, nil
			}}
			result, err := source.ConfirmedDisabledPool(ctx, sourceRequestID)
			if err != nil || calls != 1 {
				t.Fatal("typed source unavailable", err)
			}
			// Struct conversion is a test-only completeness oracle. Production uses an
			// explicit field list so future unrelated fields cannot silently gain authority.
			expected := admission.DisabledPoolFacts(facts)
			if !reflect.DeepEqual(result, expected) {
				t.Fatalf("adapter omitted/normalized source fields or refreshed old time: %+v", result)
			}
		})
	}
}
func TestPoolRouteSourceErrorsReturnZeroFactsAndNoPrivateError(t *testing.T) {
	for _, wrongRequest := range []bool{false, true} {
		facts := sourceFactsFixture()
		source := &poolRouteSource{confirmed: func(context.Context, string) (garmmanagement.DisabledPoolFacts, error) {
			if wrongRequest {
				facts.RequestID = "44444444-4444-4444-8444-444444444444"
				return facts, nil
			}
			return facts, errors.New("private endpoint and credential")
		}}
		result, err := source.ConfirmedDisabledPool(context.Background(), sourceRequestID)
		if !errors.Is(err, admission.ErrPoolRoute) || strings.Contains(err.Error(), "private") || !reflect.DeepEqual(result, admission.DisabledPoolFacts{}) {
			t.Fatal("partial/mismatched facts or raw error escaped")
		}
	}
}
func TestPoolRouteSourceRejectsNilManagerAndHasNoConstructionIO(t *testing.T) {
	if source, err := newPoolRouteSource(nil); source != nil || !errors.Is(err, admission.ErrPoolRoute) {
		t.Fatal("nil manager accepted")
	}
	// Merely taking the method value cannot invoke IO, initialize a journal or
	// inspect source/config. Production supplies New's fully bound manager.
	if source, err := newPoolRouteSource(new(garmmanagement.Manager)); source == nil || err != nil {
		t.Fatal("constructor performed source work", err)
	}
	for _, source := range []*poolRouteSource{nil, {}} {
		facts, err := source.ConfirmedDisabledPool(context.Background(), sourceRequestID)
		if !errors.Is(err, admission.ErrPoolRoute) || !reflect.DeepEqual(facts, admission.DisabledPoolFacts{}) {
			t.Fatal("unconfigured adapter produced facts")
		}
	}
}
func TestPoolRouteSourceForwardsCancellationOnceAndReturnsNoFacts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	source := &poolRouteSource{confirmed: func(actual context.Context, _ string) (garmmanagement.DisabledPoolFacts, error) {
		calls++
		if actual.Err() != context.Canceled {
			t.Fatal("caller cancellation lost")
		}
		return sourceFactsFixture(), actual.Err()
	}}
	facts, err := source.ConfirmedDisabledPool(ctx, sourceRequestID)
	if !errors.Is(err, admission.ErrPoolRoute) || calls != 1 || !reflect.DeepEqual(facts, admission.DisabledPoolFacts{}) {
		t.Fatal("canceled source retried or granted facts")
	}
}
