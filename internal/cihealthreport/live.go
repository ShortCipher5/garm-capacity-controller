package cihealthreport

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

// liveReasonProducerTrust is the standing reason live display is unavailable:
// producer trust and the exact unexpired parent attempt cannot be verified
// with the existing authorized read scope.
const liveReasonProducerTrust = "producer_trust_unverified"

// LiveAvailability reports whether live test report display may be enabled
// for one exact attempt. Availability additionally requires the exact
// unexpired parent attempt; an expired, revoked or mismatched parent attempt
// refuses even where producer trust exists.
type LiveAvailability struct {
	Available bool
	// Reason is a bounded atom naming the blocking precondition; empty when
	// available.
	Reason string
}

// CheckLiveAvailability gates live display of test-level results. It always
// reports unavailable in this build: enabling requires an independently
// established producer trust binding plus the exact unexpired parent attempt,
// and no current API or workflow establishes either without out-of-band
// enrollment that does not exist yet. Callers must not render live test
// results while this reports unavailable; retained, reviewed fixtures stay
// the only source of test-level evidence.
func CheckLiveAvailability(_ ReportScope, _ cihealth.AttemptKey) LiveAvailability {
	return LiveAvailability{Available: false, Reason: liveReasonProducerTrust}
}
