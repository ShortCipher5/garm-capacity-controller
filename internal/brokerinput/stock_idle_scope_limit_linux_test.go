package brokerinput

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

func TestStockIdlePreviewScopeLimitReason(t *testing.T) {
	for _, dimension := range []string{"configured_pools", "repositories", "observed_pools"} {
		for _, count := range []int{0, 8, 9} {
			t.Run(fmt.Sprintf("%s/%d", dimension, count), func(t *testing.T) {
				state := admission.BrokerGrantState{}
				observed := garmapi.DemandObservation{}
				switch dimension {
				case "configured_pools":
					state.Config.Pools = map[string]admission.Pool{}
					for i := 0; i < count; i++ {
						state.Config.Pools[fmt.Sprint(i)] = admission.Pool{}
					}
				case "repositories":
					state.Policy.Repositories = make([]admission.BrokerRepositoryPolicy, count)
				case "observed_pools":
					observed.Pools = make([]garmapi.DiscoveredPool, count) // Disabled rows still count.
				}
				samples, err := sampleStockIdlePreview(state, nil, observed, "", capacitybroker.Options{})
				if !errors.Is(err, capacitybroker.ErrSnapshot) {
					t.Fatal("lost snapshot classification", err)
				}
				if errors.Is(err, errStockIdleScopeLimit) != (count > 8) {
					t.Fatal("incorrect limit classification", err)
				}
				h := stockIdleHistory{prior: map[string]stockIdleSample{"old": {}}}
				now := time.Now()
				out := h.accept(samples, err, now, now.Add(time.Minute))
				want := "evidence_unavailable"
				if count > 8 {
					want = "scope_limit_exceeded"
				}
				if out.State != "unavailable" || out.Reason != want || out.HostEvidence != "" || len(out.Proposals) != 0 || h.prior != nil {
					t.Fatal(out)
				}
			})
		}
	}
}
func TestStockIdlePreviewOtherErrorsStayGeneric(t *testing.T) {
	now := time.Now()
	for _, err := range []error{nil, capacitybroker.ErrSnapshot, errors.New("scope limit exceeded"), fmt.Errorf("unrelated: %w", capacitybroker.ErrSnapshot)} {
		var h stockIdleHistory
		out := h.accept(nil, err, now, now.Add(time.Minute))
		if out.Reason != "evidence_unavailable" {
			t.Fatal(out)
		}
	}
}
