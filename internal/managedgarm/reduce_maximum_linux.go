package managedgarm

import (
	"encoding/json"
	"strings"
)

// stockPoolUpdate is the complete existing managed PUT payload. Do not add a
// field merely because stock Garm supports it; this session can pause or reduce
// the maximum, but it never changes idle settings, re-enables or raises limits.
type stockPoolUpdate struct {
	Maximum *uint64 `json:"max_runners,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
}

func decodeStockPoolUpdate(raw []byte) (stockPoolUpdate, error) {
	if len(raw) > 4096 {
		return stockPoolUpdate{}, ErrBoundary
	}
	fields, e := keyedJSONObject(raw, true)
	if e != nil || len(fields) == 0 {
		return stockPoolUpdate{}, ErrBoundary
	}
	var u stockPoolUpdate
	for key, value := range fields {
		switch key {
		case "max_runners":
			if json.Unmarshal(value, &u.Maximum) != nil || u.Maximum == nil || *u.Maximum > 1000000 {
				return stockPoolUpdate{}, ErrBoundary
			}
		case "enabled":
			if json.Unmarshal(value, &u.Enabled) != nil || u.Enabled == nil || *u.Enabled {
				return stockPoolUpdate{}, ErrBoundary
			}
		default:
			return stockPoolUpdate{}, ErrBoundary
		}
	}
	return u, nil
}

// validateMaximumReduction consumes only the exact stock identity and tuple.
// An alias cannot supply or shadow one of these fields. Other stock fields are
// not used as authority; no instance observation releases or deletes anything.
// Pinned stock source omits zero min-idle, but our topology check does not
// establish which serializer the running image uses. Absence is not zero.
func validateMaximumReduction(raw []byte, pool string, target uint64) error {
	fields, e := keyedJSONObject(raw, true)
	if e != nil {
		return ErrBoundary
	}
	var id string
	var maximum, minimum *uint64
	for _, fixed := range []struct {
		name string
		dest any
	}{{"id", &id}, {"max_runners", &maximum}, {"min_idle_runners", &minimum}} {
		for key := range fields {
			if key != fixed.name && strings.EqualFold(key, fixed.name) {
				return ErrBoundary
			}
		}
		if json.Unmarshal(fields[fixed.name], fixed.dest) != nil {
			return ErrBoundary
		}
	}
	if id != pool || maximum == nil || minimum == nil || target == 0 || target > 1000000 || *maximum > 1000000 || *minimum > *maximum || target >= *maximum || target < *minimum {
		return ErrBoundary
	}
	return nil
}
