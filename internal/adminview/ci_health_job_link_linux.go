package adminview

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealthsource"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
)

// PublishLiveTiming publishes bounded timing plus opaque handles, never job
// URLs. Only the exact supported reader and live cycle can supply a locator.
func (s *CIHealthSnapshot) PublishLiveTiming(cycle cihealthsource.Cycle, reader *demandobserver.CIHealthSource) error {
	return s.publishTiming(cycle.ObservedAt(), cycle.Facts(), cycle.State() == cihealthsource.CyclePartial,
		func(key cihealth.JobKey) (func() (string, bool), bool) {
			link, ok := cycle.ObservedJobLink(reader, key)
			if !ok {
				return nil, false
			}
			return link.URL, true
		})
}
