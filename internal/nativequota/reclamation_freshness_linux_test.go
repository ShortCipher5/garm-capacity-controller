//go:build linux

package nativequota

import (
	"testing"
	"time"
)

func TestReclamationFreshnessUsesSamplingStart(t *testing.T) {
	if (ReclamationObservation{}).Fresh() {
		t.Fatal("zero observation accepted")
	}
	for _, at := range []time.Time{time.Now().Add(-6 * time.Second), time.Now().Add(time.Second)} {
		if (ReclamationObservation{at: at}).Fresh() {
			t.Fatal("stale/future accepted")
		}
	}
	o := ReclamationObservation{at: time.Now()}
	if !o.Fresh() {
		t.Fatal("fresh rejected")
	}
	if o.ObservedAt().Location() != time.UTC {
		t.Fatal("noncanonical timestamp")
	}
}
