package admission

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// RecordSSHInventoryAudit records only correlation and outcome, never a host key,
// address, credential, raw output or authority to enroll/activate a host.
func (s *SQLiteStore) RecordSSHInventoryAudit(ctx context.Context, request, target, digest, phase string, available bool) error {
	decoded, e := hex.DecodeString(digest)
	if e != nil || len(decoded) != 32 || digest != strings.ToLower(digest) {
		return ErrDenied
	}
	if !ValidOnboardingRequestID(request) || !ValidOnboardingProfileID(target) || (phase != "requested" && phase != "completed") || (phase == "requested" && available) {
		return ErrDenied
	}
	data, e := json.Marshal(struct {
		Actor     string `json:"actor"`
		Request   string `json:"request_id"`
		Target    string `json:"target_id"`
		Phase     string `json:"phase"`
		Digest    string `json:"review_digest"`
		Available bool   `json:"observation_available"`
	}{"ssh-inventory-token", request, target, phase, digest, available})
	if e != nil {
		return e
	}
	return s.transaction(ctx, func(tx *sql.Tx) error { return sqliteAudit(tx, "ssh_inventory_"+phase, string(data), s.now()) })
}
