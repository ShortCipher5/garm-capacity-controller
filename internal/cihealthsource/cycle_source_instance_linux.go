package cihealthsource

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

// cycleSourceInstance is an in-process link to one exact supported reader.
// SourceID is useful long-lived metadata, but is shared by same-origin readers.
// The read interface remains open for timing only: no arbitrary implementation
// can mint this link, and we never compare arbitrary interface values.
type cycleSourceInstance struct {
	repair   *demandrepair.CIHealthSource
	observer *demandobserver.CIHealthSource
}

func captureCycleSource(source Source) cycleSourceInstance {
	switch s := source.(type) {
	case *demandrepair.CIHealthSource:
		return cycleSourceInstance{repair: s}
	case *demandobserver.CIHealthSource:
		return cycleSourceInstance{observer: s}
	default:
		return cycleSourceInstance{}
	}
}
