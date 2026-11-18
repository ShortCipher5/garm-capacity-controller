package brokerinput

import (
	"fmt"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

func TestCycleExpiryBoundedAndNeverSelectsConsumed(t *testing.T) {
	now := time.Now()
	grants := []admission.BrokerGrant{}
	for _, state := range []string{"consumed", "revoked", "expired", "unknown"} {
		grants = append(grants, admission.BrokerGrant{ID: state, State: state, Spec: admission.BrokerGrantSpec{ExpiresAt: now.Add(-time.Hour)}})
	}
	grants = append(grants, admission.BrokerGrant{ID: "future", State: "unused", Spec: admission.BrokerGrantSpec{ExpiresAt: now.Add(time.Hour)}})
	grants = append(grants, admission.BrokerGrant{ID: "missing-time", State: "unused"})
	for i := 0; i < 1000; i++ {
		grants = append(grants, admission.BrokerGrant{ID: fmt.Sprintf("eligible-%d", i), State: "unused", Spec: admission.BrokerGrantSpec{ExpiresAt: now}})
	}
	ids := cycleExpiryCandidates(grants, now)
	if len(ids) != cycleExpiryLimit || ids[0] != "eligible-0" || ids[len(ids)-1] != "eligible-63" {
		t.Fatalf("unsafe or unbounded expiry %+v", ids)
	}
	if len(cycleExpiryCandidates(grants, now.Add(-time.Second))) != 0 {
		t.Fatal("future expiry selected")
	}
}
