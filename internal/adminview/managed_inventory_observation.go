package adminview

import "time"

// ManagedInventoryObservation reports a fresh read from the configured stock
// inventory scope, never physical admission, capacity or readiness.
type ManagedInventoryObservation struct {
	Available bool       `json:"available"`
	SampledAt *time.Time `json:"sampled_at,omitempty"`
	Count     *int       `json:"count,omitempty"`
}

// SetManagedInventoryObservation must be called before serving requests.
// A nil observer omits the optional, read-only overview field.
func (h *Handler) SetManagedInventoryObservation(sample func(time.Time) ManagedInventoryObservation) {
	h.managedInventoryObservation = sample
}
