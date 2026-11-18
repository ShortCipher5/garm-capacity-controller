package nativeidentity

import (
	"context"
	"time"
)

// RetirementOperationRecord is pinned-owner native-operation history, not proof
// that the guest cgroup or physical storage no longer exists.
type RetirementOperationRecord struct {
	PermitID      string `json:"permit_id"`
	IntentDigest  string `json:"intent_digest"`
	ClaimID       string `json:"claim_id"`
	RequestDigest string `json:"request_digest"`
	DomainID      string `json:"domain_id"`
	PolicyDigest  string `json:"policy_digest"`
	NativeName    string `json:"native_name"`
	ResourceID    string `json:"resource_id"`
	Dataset       string `json:"dataset"`
	DatasetGUID   string `json:"dataset_guid"`
	Step          string `json:"step"`
	OperationID   string `json:"operation_id"`
	Outcome       string `json:"outcome"`
}
type RetirementOperationObservation struct {
	record RetirementOperationRecord
	leaf   string
	at     time.Time
	valid  bool
}

func (o RetirementOperationObservation) Valid() bool                       { return o.valid }
func (o RetirementOperationObservation) Record() RetirementOperationRecord { return o.record }
func (o RetirementOperationObservation) OwnerServerLeaf() string           { return o.leaf }
func (o RetirementOperationObservation) ObservedAt() time.Time             { return o.at }
func ObserveRetirementOperation(ctx context.Context, c Configuration, id string) (RetirementOperationObservation, error) {
	return observeRetirementOperation(ctx, c, id, false)
}
func ObserveRetirementTerminal(ctx context.Context, c Configuration, id string) (RetirementOperationObservation, error) {
	return observeRetirementOperation(ctx, c, id, true)
}
func observeRetirementOperation(ctx context.Context, c Configuration, id string, terminal bool) (RetirementOperationObservation, error) {
	if !validHash(id) {
		return RetirementOperationObservation{}, ErrObservation
	}
	path := "/v1/native/retirement-operations/" + id
	if terminal {
		path += "/terminal"
	}
	raw, at, e := readOwnerEvidence(ctx, c, path)
	if e != nil {
		return RetirementOperationObservation{}, e
	}
	var r RetirementOperationRecord
	if strictEvidence(raw, &r, []string{"permit_id", "intent_digest", "claim_id", "request_digest", "domain_id", "policy_digest", "native_name", "resource_id", "dataset", "dataset_guid", "step", "operation_id", "outcome"}) != nil || !validPermit(r.PermitID) || !validHash(r.IntentDigest) || !validHash(r.RequestDigest) || r.ClaimID != id || r.DomainID != c.DomainID || r.PolicyDigest != c.PolicyDigest || !validName(r.NativeName) || !validOperationID(r.ResourceID) || r.Dataset == "" || r.DatasetGUID == "" || (r.Step != "stop" && r.Step != "delete") {
		return RetirementOperationObservation{}, ErrObservation
	}
	if r.Outcome != "accepted" && r.Outcome != "succeeded" && r.Outcome != "failed" && r.Outcome != "unknown" {
		return RetirementOperationObservation{}, ErrObservation
	}
	if (r.Outcome != "unknown" || r.OperationID != "") && !validOperationID(r.OperationID) {
		return RetirementOperationObservation{}, ErrObservation
	}
	if (!terminal && r.Outcome != "accepted") || (terminal && r.Outcome == "accepted") {
		return RetirementOperationObservation{}, ErrObservation
	}
	return RetirementOperationObservation{r, c.OwnerServerLeaf, at, true}, nil
}
