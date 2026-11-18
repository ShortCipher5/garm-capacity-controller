package managedgarm

import (
	"encoding/json"
	"reflect"
)

// restartStep is a durable desired transition, not proof that Docker performed
// it. A dispatched transition may never be retried merely because its reply was
// lost. Recovery must inspect the daemon and process identities independently.
type restartStep struct {
	Container string `json:"container"`
	Action    string `json:"action"`
}

func managedRestartOrder(s Specification) ([]restartStep, error) {
	if !validSpec(s) {
		return nil, ErrBoundary
	}
	return []restartStep{
		{s.IngressID, "stop"}, {s.ControllerID, "stop"}, {s.GarmID, "stop"},
		{s.GarmID, "start"}, {s.ControllerID, "start"}, {s.IngressID, "start"},
	}, nil
}

type restartRecord struct {
	Version         string                        `json:"version"`
	Specification   Specification                 `json:"specification"`
	ProfileSnapshot json.RawMessage               `json:"profile_snapshot"`
	Files           map[string]sealedFileIdentity `json:"sealed_files"`
	OriginalPID     int                           `json:"original_pid"`
	OriginalStart   uint64                        `json:"original_start"`
	Steps           []restartStep                 `json:"steps"`
	Completed       int                           `json:"completed"`
	Pending         bool                          `json:"pending"`
	State           string                        `json:"state"`
}

// advanceRestart is pure journal transition validation. Actual effects belong
// only in the concrete root Docker coordinator; this never mints authority.
func advanceRestart(r restartRecord, dispatched, confirmed bool) (restartRecord, error) {
	order, err := managedRestartOrder(r.Specification)
	if err != nil || r.Version != "managed-restart/v1" || !reflect.DeepEqual(order, r.Steps) || r.Completed < 0 || r.Completed > len(order) || r.State != "prepared" || r.Pending {
		return restartRecord{}, ErrBoundary
	}
	if r.Completed == len(order) || !dispatched {
		return restartRecord{}, ErrBoundary
	}
	r.Pending = true
	if confirmed {
		r.Pending = false
		r.Completed++
	}
	return r, nil
}
