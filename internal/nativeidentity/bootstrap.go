package nativeidentity

import (
	"context"
	"time"
)

// BootstrapRecord describes actual stopped-instance file readback by the owner.
// Verified is NOT fsync, boot success, accepted job, or physical release proof.
type BootstrapRecord struct {
	ClaimID          string `json:"claim_id"`
	PlanDigest       string `json:"plan_digest"`
	PermitID         string `json:"permit_id"`
	EnvelopeDigest   string `json:"envelope_digest"`
	ParentClaimID    string `json:"parent_claim_id"`
	DomainID         string `json:"domain_id"`
	PolicyDigest     string `json:"policy_digest"`
	NativeName       string `json:"native_name"`
	ResourceID       string `json:"resource_id"`
	ImageFingerprint string `json:"image_fingerprint"`
	Outcome          string `json:"outcome"`
	LaunchSHA256     string `json:"launch_sha256"`
	LaunchBytes      int64  `json:"launch_bytes"`
	JITSHA256        string `json:"jit_sha256"`
	JITBytes         int64  `json:"jit_bytes"`
	LaunchUID        int64  `json:"launch_uid"`
	LaunchGID        int64  `json:"launch_gid"`
	LaunchMode       string `json:"launch_mode"`
	LaunchType       string `json:"launch_type"`
	JITUID           int64  `json:"jit_uid"`
	JITGID           int64  `json:"jit_gid"`
	JITMode          string `json:"jit_mode"`
	JITType          string `json:"jit_type"`
}

func (r BootstrapRecord) Valid() bool {
	if !validHash(r.ClaimID) || !validHash(r.PlanDigest) || !validPermit(r.PermitID) || !validHash(r.EnvelopeDigest) || !validHash(r.ParentClaimID) || !validHash(r.DomainID) || !validHash(r.PolicyDigest) || !validName(r.NativeName) || !validOperationID(r.ResourceID) || !validHash(r.ImageFingerprint) || !validHash(r.LaunchSHA256) || !validHash(r.JITSHA256) || r.LaunchBytes <= 0 || r.LaunchBytes > 1<<20 || r.JITBytes <= 0 || r.JITBytes > 1<<20 {
		return false
	}
	if r.Outcome == "unknown" {
		return true
	}
	return r.Outcome == "verified" && r.LaunchUID == 0 && r.LaunchGID == 0 && r.LaunchMode == "0600" && r.LaunchType == "file" && r.JITUID == 0 && r.JITGID == 0 && r.JITMode == "0600" && r.JITType == "file"
}

type BootstrapObservation struct {
	record     BootstrapRecord
	serverLeaf string
	at         time.Time
	valid      bool
}

func (o BootstrapObservation) Valid() bool { return o.valid && !o.at.IsZero() }
func (o BootstrapObservation) Record() BootstrapRecord {
	if !o.Valid() {
		return BootstrapRecord{}
	}
	return o.record
}
func (o BootstrapObservation) ObservedAt() time.Time   { return o.at }
func (o BootstrapObservation) OwnerServerLeaf() string { return o.serverLeaf }
func ObserveBootstrap(ctx context.Context, c Configuration, claimID string) (BootstrapObservation, error) {
	if !validHash(claimID) {
		return BootstrapObservation{}, ErrObservation
	}
	raw, at, e := readOwnerEvidence(ctx, c, "/v1/native/bootstraps/"+claimID)
	if e != nil {
		return BootstrapObservation{}, e
	}
	var r BootstrapRecord
	if strictEvidence(raw, &r, []string{"claim_id", "plan_digest", "permit_id", "envelope_digest", "parent_claim_id", "domain_id", "policy_digest", "native_name", "resource_id", "image_fingerprint", "outcome", "launch_sha256", "launch_bytes", "jit_sha256", "jit_bytes", "launch_uid", "launch_gid", "launch_mode", "launch_type", "jit_uid", "jit_gid", "jit_mode", "jit_type"}) != nil || !r.Valid() || r.ClaimID != claimID || r.DomainID != c.DomainID || r.PolicyDigest != c.PolicyDigest {
		return BootstrapObservation{}, ErrObservation
	}
	return BootstrapObservation{r, c.OwnerServerLeaf, at, true}, nil
}
