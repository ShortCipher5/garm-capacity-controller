// Package reuse defines the verified teardown contract between observation
// packages and capacity accounting: which original charge a runner teardown
// claim addresses, which positive evidence qualifies it, and which resource
// dimensions may be returned to the fleet. It contains no transport, no
// ledger access and no release authority; a missing or unverifiable fact
// always keeps the whole charge held.
package reuse

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode"
	"unicode/utf8"
)

// Dimensions selects the resource dimensions of one charge. A true value
// means qualified positive evidence proved that dimension reusable; it is
// never inferred from absence, timeout, a delete acknowledgement, a depleted
// page, a reduced pool limit or a missing row.
type Dimensions struct {
	CPU     bool `json:"cpu"`
	RAM     bool `json:"ram"`
	Runners bool `json:"runners"`
	Disk    bool `json:"disk"`
}

// All reports whether every dimension is qualified. A charge with no disk
// request reaches All only through the disk normalization in Assess.
func (d Dimensions) All() bool { return d.CPU && d.RAM && d.Runners && d.Disk }

// Any reports whether at least one dimension is qualified.
func (d Dimensions) Any() bool { return d.CPU || d.RAM || d.Runners || d.Disk }

// Held reason vocabulary. Assess returns these exact strings, sorted and
// deduplicated, so consumers can map them to stable operator guidance.
const (
	HeldInvalidCharge             = "invalid_charge"
	HeldEvidenceForDistinctCharge = "evidence_for_distinct_charge"
	HeldSameNameReplacement       = "same_name_replacement_is_distinct_charge"
	HeldConflictingEvidence       = "conflicting_evidence"
	HeldChronologyConflict        = "chronology_conflict"
	HeldAwaitingCompletedWorkload = "awaiting_completed_workload"
	HeldAwaitingTerminalDelete    = "awaiting_terminal_delete_observation"
	HeldAwaitingPostDeleteAbsence = "awaiting_post_delete_absence"
	HeldResidualVolumeUnverified  = "residual_volume_unverified"
)

func digest64(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// incarnationID matches the authenticated transport grammar for immutable
// instance incarnation identifiers (UUID and generation).
func incarnationID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
		} else if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// operationUUID matches the authenticated transport grammar for provider
// operation identifiers.
func operationUUID(value string) bool { return incarnationID(value) }

// instanceName matches the bounded non-secret instance naming grammar.
func instanceName(value string) bool {
	if len(value) == 0 || len(value) > 63 {
		return false
	}
	for i, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || (r == '-' && i > 0)) {
			return false
		}
	}
	return true
}

// volumeName bounds an exact charged allocation name (an Incus volume name,
// which is the stock Incus provider resource ID). It is display data plus an
// exact collection key, never identity proof by itself.
func volumeName(value string) bool {
	if len(value) == 0 || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == '/' {
			return false
		}
	}
	return true
}

// label bounds enrolled scope identifiers such as project and member names.
func label(value string) bool {
	if len(value) == 0 || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// segment matches one GitHub repository path segment.
func segment(value string) bool {
	if len(value) == 0 || len(value) > 100 || value[0] == '-' || value[0] == '.' {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-') {
			return false
		}
	}
	return true
}

func canonicalDigest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
