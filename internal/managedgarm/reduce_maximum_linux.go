package managedgarm

import (
	"encoding/json"
)

// validateMaximumReduction parses only authoritative stock fields. Missing or
// ambiguous values never become a zero/default authorization. No instance list
// or presumed idle state is used to release/delete anything.
func validateMaximumReduction(raw []byte, pool string, target uint64) error {
	var current struct {
		ID      string  `json:"id"`
		Maximum *uint64 `json:"max_runners"`
		Minimum *uint64 `json:"min_idle_runners"`
	}
	_, objectErr := metadataObject(raw)
	if objectErr != nil || json.Unmarshal(raw, &current) != nil || current.ID != pool || current.Maximum == nil || current.Minimum == nil || target == 0 || target > 1000000 || *current.Maximum > 1000000 || *current.Minimum > *current.Maximum || target >= *current.Maximum || target < *current.Minimum {
		return ErrBoundary
	}
	return nil
}
