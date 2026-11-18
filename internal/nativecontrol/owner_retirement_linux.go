//go:build linux

package nativecontrol

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"io"
	"net/http"
	"strings"
)

type ownerRetirementStep struct {
	claim             admission.NativeRetirementClaim
	operation         nativeapi.RetirementOperation
	accepted, current nativeidentity.RetirementOperationRecord
}
type ownerRetirement struct {
	intent               admission.NativeRetirementIntent
	stopUsed, deleteUsed bool
	stop, deleted        *ownerRetirementStep
	accounting           *nativeapi.StorageReclamationObservation
}

func retirementMatches(x *ownerExecution, in admission.NativeRetirementIntent) bool {
	if x == nil || x.start == nil || x.start.current.Outcome != "succeeded" || !x.quota.Valid() {
		return false
	}
	p := x.parent
	return in.Version == "native-retirement/v1" && in.PermitID == p.Envelope.PermitID && in.EnvelopeDigest == p.EnvelopeDigest && in.RequestKey == p.Envelope.RequestKey && in.DomainID == p.Envelope.DomainID && in.PolicyDigest == p.Envelope.PolicyDigest && in.NativeName == p.Envelope.NativeName && in.Epoch == p.Envelope.ReconciliationEpoch && in.ResourceID == x.quota.ResourceID() && in.Dataset == x.quota.Dataset() && in.DatasetGUID == x.quota.DatasetGUID() && in.StartOperationID == x.start.current.OperationID && x.guest.ResourceID() == in.ResourceID && x.guest.DomainID() == in.DomainID && hash(in.RootFenceDigest) && !in.RequestedAt.IsZero()
}
func (o *executionOwner) retire(ctx context.Context, in admission.NativeRetirementIntent, step string) (string, error) {
	o.mu.Lock()
	x := o.permits[in.PermitID]
	o.mu.Unlock()
	if x == nil {
		return "", ErrControl
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if !retirementMatches(x, in) || o.api.CheckBoundary(ctx) != nil {
		return "", ErrControl
	}
	if x.retirement == nil {
		x.retirement = &ownerRetirement{intent: in}
	}
	prior, _ := json.Marshal(x.retirement.intent)
	current, _ := json.Marshal(in)
	if string(prior) != string(current) {
		return "", ErrControl
	}
	t := x.retirement
	switch step {
	case admission.NativeRetireStop:
		if t.stopUsed {
			return "", ErrControl
		}
		t.stopUsed = true
	case admission.NativeRetireDelete:
		if t.deleteUsed {
			return "", ErrControl
		}
		t.deleteUsed = true
		if t.stop == nil || t.stop.current.Outcome != "succeeded" {
			return "", ErrControl
		}
		exit, e := o.api.ObservePIDNamespaceTermination(ctx, x.prepared, x.guest)
		if e != nil || !exit.Terminated() || exit.ResourceID() != in.ResourceID || exit.DomainID() != in.DomainID {
			return "", ErrControl
		}
	default:
		return "", ErrControl
	}
	claim, e := o.claims.retirement(ctx, in, step)
	if e != nil {
		return "", ErrControl
	}
	record := nativeidentity.RetirementOperationRecord{PermitID: in.PermitID, IntentDigest: claim.IntentDigest, ClaimID: claim.ID, RequestDigest: claim.RequestDigest, DomainID: in.DomainID, PolicyDigest: in.PolicyDigest, NativeName: in.NativeName, ResourceID: in.ResourceID, Dataset: in.Dataset, DatasetGUID: in.DatasetGUID, Step: step, Outcome: "unknown"}
	s := &ownerRetirementStep{claim: claim, accepted: record, current: record}
	if step == admission.NativeRetireStop {
		t.stop = s
	} else {
		t.deleted = s
	}
	o.mu.Lock()
	if o.retirementSteps == nil {
		o.retirementSteps = map[string]*ownerExecution{}
	}
	if o.retirementSteps[claim.ID] != nil {
		o.mu.Unlock()
		return claim.ID, ErrControl
	}
	o.retirementSteps[claim.ID] = x
	o.mu.Unlock()
	var operation nativeapi.RetirementOperation
	if step == admission.NativeRetireStop {
		operation, e = o.api.StopForRetirement(ctx, x.prepared, x.quota, o.quota)
	} else {
		operation, e = o.api.DeleteForRetirement(ctx, x.prepared, t.stop.operation, o.quota)
	}
	if e != nil {
		return claim.ID, ErrControl
	}
	s.operation = operation
	s.accepted.OperationID = operation.OperationID()
	s.accepted.Outcome = "accepted"
	s.current = s.accepted
	return claim.ID, nil
}
func (o *executionOwner) serveRetirementOperation(w http.ResponseWriter, r *http.Request) bool {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/native/"), "/")
	if len(parts) == 3 && parts[0] == "executions" && (parts[2] == "retire-stop" || parts[2] == "retire-delete") {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "canonical POST required", 400)
			return true
		}
		raw, e := io.ReadAll(io.LimitReader(r.Body, 16385))
		var in admission.NativeRetirementIntent
		if e != nil || len(raw) > 16384 || decodeCanonical(raw, &in) != nil || in.PermitID != parts[1] {
			http.Error(w, "invalid retirement intent", 400)
			return true
		}
		step := admission.NativeRetireStop
		if parts[2] == "retire-delete" {
			step = admission.NativeRetireDelete
		}
		id, e := o.retire(r.Context(), in, step)
		if e != nil {
			http.Error(w, "retirement unknown; no retry", 409)
			return true
		}
		respond(w, 201, map[string]string{"retirement_claim_id": id})
		return true
	}
	if (len(parts) != 2 && len(parts) != 3) || parts[0] != "retirement-operations" {
		return false
	}
	if r.Method != http.MethodGet || !hash(parts[1]) || (len(parts) == 3 && parts[2] != "terminal" && parts[2] != "storage-accounting") {
		http.Error(w, "invalid operation read", 400)
		return true
	}
	o.mu.Lock()
	x := o.retirementSteps[parts[1]]
	o.mu.Unlock()
	if x == nil {
		http.NotFound(w, r)
		return true
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	var s *ownerRetirementStep
	for _, candidate := range []*ownerRetirementStep{x.retirement.stop, x.retirement.deleted} {
		if candidate != nil && candidate.claim.ID == parts[1] {
			s = candidate
		}
	}
	if s == nil {
		http.NotFound(w, r)
		return true
	}
	if len(parts) == 3 && parts[2] == "storage-accounting" {
		report, e := o.retirementStorageAccounting(r.Context(), x, s)
		if e != nil {
			http.Error(w, "storage accounting unavailable; all unresolved charge remains", 409)
		} else {
			respond(w, 200, report)
		}
		return true
	}
	if len(parts) == 2 {
		respond(w, 200, s.accepted)
		return true
	}
	if s.current.Outcome == "accepted" {
		op, e := o.api.ObserveRetirementOperation(r.Context(), x.prepared, s.operation)
		if e != nil {
			s.current.Outcome = "unknown"
		} else {
			s.operation = op
			s.current.Outcome = op.Outcome()
		}
	}
	respond(w, 200, s.current)
	return true
}
