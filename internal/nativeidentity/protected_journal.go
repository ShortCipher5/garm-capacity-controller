package nativeidentity

import (
	"context"
	"time"
)

// ProtectedJournalRecord is root-file readback only. No field grants release.
type ProtectedJournalRecord struct {
	PermitID         string    `json:"permit_id"`
	EnvelopeDigest   string    `json:"envelope_digest"`
	DomainID         string    `json:"domain_id"`
	PolicyDigest     string    `json:"policy_digest"`
	NativeName       string    `json:"native_name"`
	ResourceID       string    `json:"resource_id"`
	ImageFingerprint string    `json:"image_fingerprint"`
	InitSHA256       string    `json:"init_sha256"`
	State            string    `json:"state"`
	RecordAt         time.Time `json:"record_at"`
	ExitCode         int       `json:"exit_code"`
}
type ProtectedJournalObservation struct {
	record ProtectedJournalRecord
	leaf   string
	at     time.Time
	valid  bool
}

func (o ProtectedJournalObservation) Valid() bool                    { return o.valid }
func (o ProtectedJournalObservation) Record() ProtectedJournalRecord { return o.record }
func (o ProtectedJournalObservation) OwnerServerLeaf() string        { return o.leaf }
func (o ProtectedJournalObservation) ObservedAt() time.Time          { return o.at }
func ObserveProtectedJournal(ctx context.Context, c Configuration, permit string) (ProtectedJournalObservation, error) {
	if !validPermit(permit) {
		return ProtectedJournalObservation{}, ErrObservation
	}
	raw, at, e := readOwnerEvidence(ctx, c, "/v1/native/executions/"+permit+"/journal")
	if e != nil {
		return ProtectedJournalObservation{}, e
	}
	var r ProtectedJournalRecord
	if strictEvidence(raw, &r, []string{"permit_id", "envelope_digest", "domain_id", "policy_digest", "native_name", "resource_id", "image_fingerprint", "init_sha256", "state", "record_at", "exit_code"}) != nil || r.PermitID != permit || !validHash(r.EnvelopeDigest) || r.DomainID != c.DomainID || r.PolicyDigest != c.PolicyDigest || !validName(r.NativeName) || !validOperationID(r.ResourceID) || !validHash(r.ImageFingerprint) || !validHash(r.InitSHA256) || r.RecordAt.IsZero() || r.RecordAt.After(at) {
		return ProtectedJournalObservation{}, ErrObservation
	}
	if (r.State != "launch_unknown" || r.ExitCode != -1) && (r.State != "process_tree_exited_unqualified" || r.ExitCode < 0 || r.ExitCode > 255) {
		return ProtectedJournalObservation{}, ErrObservation
	}
	return ProtectedJournalObservation{r, c.OwnerServerLeaf, at, true}, nil
}
