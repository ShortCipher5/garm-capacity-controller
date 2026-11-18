//go:build linux

package nativecontrol

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeartifacts"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeenforcement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativequota"
)

const ExecutionPath = "/v1/native/executions"
const maxOwnerExecutions = 65536

// ownerExecution is never reconstructed from PID, disk JSON or controller input.
// Losing this process quarantines its domain; StartFresh cannot adopt it.
type ownerExecution struct {
	mu                  sync.Mutex
	prepared            nativeapi.Prepared
	parent              admission.FreshNativeClaim
	create, start       *ownerStep
	startUsed           bool
	quota               nativeapi.StoppedQuota
	guest               nativeapi.GuestIncarnation
	datasetLife         nativeapi.DatasetLifetime
	reclamation         nativeapi.StorageReclamationTracker
	reclamationCaptured bool
	retirement          *ownerRetirement
	userData            string
	bootstrapUsed       bool
	bootstrapClaim      admission.NativeBootstrapClaim
	bootstrapRecord     nativeidentity.BootstrapRecord
}
type ownerStep struct {
	claim    admission.NativeOperationClaim
	op       nativeapi.Operation
	accepted nativeidentity.OperationRecord
	current  nativeidentity.OperationRecord
}
type executionOwner struct {
	retirementSteps map[string]*ownerExecution
	api             *nativeapi.Client
	policy          nativeapi.Policy
	claims          *ClaimClient
	quota           *nativequota.Reader
	pools           *nativequota.PoolReader
	mu              sync.Mutex
	permits         map[string]*ownerExecution
	names           map[string]bool
	steps           map[string]*ownerExecution
}
type executionRequest struct {
	Envelope admission.NativeExecutionEnvelope `json:"envelope"`
	UserData string                            `json:"user_data"`
}

// NewExecutionServer builds the owner service behind the still-closed execution
// boundary. Both pinned quota and pool readers are mandatory. It never adopts
// an owner, retries mutations or releases charge from accounting diagnostics.
func NewExecutionServer(api *nativeapi.Client, policy nativeapi.Policy, claims *ClaimClient, quota *nativequota.Reader, pools *nativequota.PoolReader, serverTLS, claimTLS *tls.Config, controllerLeaf string) (*http.Server, error) {
	if !policy.Protected() || api == nil || claims == nil || quota == nil || pools == nil || api.PolicyDigest() != policy.Digest() || claims.config.DomainID != policy.DomainID() || claims.config.PolicyDigest != policy.Digest() || fingerprint(claimTLS) != claims.config.OwnerClientLeaf {
		return nil, ErrControl
	}
	if claims.config.ControllerServerLeaf == fingerprint(serverTLS) || claims.config.ControllerServerLeaf == controllerLeaf {
		return nil, ErrControl
	}
	if api.CheckBoundary(context.Background()) != nil {
		return nil, ErrControl
	}
	server, e := NewOwnerServer(api, serverTLS, claimTLS, controllerLeaf)
	if e != nil {
		return nil, e
	}
	if api.ClaimOwner() != nil {
		return nil, ErrControl
	}
	owner := &executionOwner{api: api, policy: policy, claims: claims, quota: quota, pools: pools, permits: map[string]*ownerExecution{}, names: map[string]bool{}, steps: map[string]*ownerExecution{}}
	discovery := server.Handler
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !peer(r.TLS, controllerLeaf) {
			http.Error(w, "owner authentication required", 401)
			return
		}
		if r.URL.Path == nativeartifacts.Path {
			serveArtifacts(w, r, api, fingerprint(serverTLS), fingerprint(claimTLS))
			return
		}
		if r.URL.Path == nativeenforcement.Path {
			serveEnforcement(w, r, api, fingerprint(serverTLS), fingerprint(claimTLS))
			return
		}
		if r.URL.Path == nativeidentity.Path {
			discovery.ServeHTTP(w, r)
			return
		}
		if owner.serveRetainedEvidence(w, r, fingerprint(serverTLS), fingerprint(claimTLS)) {
			return
		}
		if r.URL.RawQuery != "" {
			http.Error(w, "query not supported", 400)
			return
		}
		owner.serve(w, r)
	})
	return server, nil
}
func (o *executionOwner) serve(w http.ResponseWriter, r *http.Request) {
	if o.serveRetirementOperation(w, r) {
		return
	}
	if r.URL.Path == ExecutionPath && r.Method == http.MethodPost {
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "JSON required", 415)
			return
		}
		raw, e := io.ReadAll(io.LimitReader(r.Body, 2<<20+1))
		var in executionRequest
		if e != nil || len(raw) > 2<<20 || decodeCanonical(raw, &in) != nil {
			http.Error(w, "invalid execution", 400)
			return
		}
		id, e := o.create(r.Context(), in)
		if e != nil {
			http.Error(w, "execution unknown or unavailable; no retry", 409)
			return
		}
		respond(w, 201, map[string]string{"create_step_claim_id": id})
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/native/"), "/")
	if len(parts) == 3 && parts[0] == "executions" && parts[2] == "journal" && r.Method == http.MethodGet {
		record, e := o.protectedJournal(r.Context(), parts[1])
		if e != nil {
			http.Error(w, "protected journal unavailable", 503)
			return
		}
		respond(w, 200, record)
		return
	}
	if len(parts) == 3 && parts[0] == "executions" && (parts[2] == "start" || parts[2] == "bootstrap") && r.Method == http.MethodPost {
		raw, e := io.ReadAll(io.LimitReader(r.Body, 3))
		if e != nil || string(raw) != "{}" || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "empty canonical JSON required", 400)
			return
		}
		if parts[2] == "bootstrap" {
			id, e := o.bootstrap(r.Context(), parts[1])
			if e != nil {
				http.Error(w, "bootstrap unknown or unavailable; no retry", 409)
				return
			}
			respond(w, 201, map[string]string{"bootstrap_claim_id": id})
			return
		}
		id, e := o.start(r.Context(), parts[1])
		if e != nil {
			http.Error(w, "start unknown or unavailable; no retry", 409)
			return
		}
		respond(w, 201, map[string]string{"start_step_claim_id": id})
		return
	}
	if r.Method == http.MethodGet && len(parts) >= 2 && len(parts) <= 3 && hash(parts[1]) {
		o.mu.Lock()
		execution := o.steps[parts[1]]
		o.mu.Unlock()
		if execution == nil {
			http.NotFound(w, r)
			return
		}
		execution.mu.Lock()
		defer execution.mu.Unlock()
		if parts[0] == "bootstraps" && len(parts) == 2 && execution.bootstrapClaim.ID == parts[1] {
			respond(w, 200, execution.bootstrapRecord)
			return
		}
		step := execution.create
		if step == nil || step.claim.ID != parts[1] {
			step = execution.start
		}
		if step == nil || step.claim.ID != parts[1] {
			http.NotFound(w, r)
			return
		}
		if parts[0] == "operations" && len(parts) == 2 {
			respond(w, 200, step.accepted)
			return
		}
		if parts[0] == "operations" && len(parts) == 3 && parts[2] == "terminal" {
			o.observe(r.Context(), execution, step)
			respond(w, 200, step.current)
			return
		}
		if parts[0] == "quotas" && len(parts) == 2 && step == execution.create {
			if step.current.Outcome != "succeeded" {
				http.Error(w, "successful create required", 409)
				return
			}
			q, e := o.api.VerifyStoppedQuota(r.Context(), execution.prepared, o.quota)
			if e != nil || q.ResourceID() != step.current.ResourceID {
				http.Error(w, "quota unavailable", 409)
				return
			}
			if execution.quota.Valid() && (execution.quota.DatasetGUID() != q.DatasetGUID() || execution.quota.ResourceID() != q.ResourceID()) {
				http.Error(w, "quota incarnation changed", 409)
				return
			}
			execution.quota = q
			p := execution.parent
			respond(w, 200, nativeidentity.QuotaRecord{DatasetGUID: q.DatasetGUID(), QuotaKind: "quota", PermitID: p.Envelope.PermitID, EnvelopeDigest: p.EnvelopeDigest, ParentClaimID: p.ClaimID, CreateStepClaimID: step.claim.ID, DomainID: q.DomainID(), PolicyDigest: q.PolicyDigest(), NativeName: q.Name(), ResourceID: q.ResourceID(), CreateOperationID: step.op.ID(), Backend: "zfs", Dataset: q.Dataset(), RequestedBytes: q.Bytes(), EnforcedBytes: q.Bytes(), InstanceState: "stopped"})
			return
		}
	}
	http.NotFound(w, r)
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (o *executionOwner) create(ctx context.Context, in executionRequest) (string, error) {
	prepared, e := Prepare(o.policy, in.Envelope, in.UserData)
	if e != nil {
		return "", e
	}
	// Consume both keys BEFORE any central exchange; never delete tombstones.
	o.mu.Lock()
	if len(o.permits) >= maxOwnerExecutions || o.permits[in.Envelope.PermitID] != nil || o.names[prepared.Name()] {
		o.mu.Unlock()
		return "", ErrControl
	}
	execution := &ownerExecution{prepared: prepared, userData: in.UserData}
	execution.mu.Lock()
	o.permits[in.Envelope.PermitID] = execution
	o.names[prepared.Name()] = true
	o.mu.Unlock()
	defer execution.mu.Unlock()
	parent, e := o.claims.consume(ctx, in.Envelope)
	if e != nil {
		return "", e
	}
	execution.parent = parent
	claim, e := o.claims.step(ctx, parent, admission.NativeCreateStopped)
	if e != nil {
		return "", e
	}
	step := o.installStep(execution, claim)
	if step == nil {
		return claim.ID, ErrControl
	}
	execution.create = step
	op, e := o.api.CreateStopped(ctx, prepared)
	if e != nil {
		return claim.ID, ErrControl
	}
	step.op = op
	step.accepted.OperationID = op.ID()
	step.accepted.Outcome = "accepted"
	step.current = step.accepted
	return claim.ID, nil
}
func (o *executionOwner) installStep(execution *ownerExecution, claim admission.NativeOperationClaim) *ownerStep {
	p := execution.parent
	record := nativeidentity.OperationRecord{PermitID: claim.PermitID, EnvelopeDigest: claim.EnvelopeDigest, ParentClaimID: claim.ParentClaimID, Step: claim.Step, StepClaimID: claim.ID, DomainID: p.Envelope.DomainID, PolicyDigest: p.Envelope.PolicyDigest, NativeName: p.Envelope.NativeName, Outcome: "unknown"}
	step := &ownerStep{claim: claim, accepted: record, current: record}
	o.mu.Lock()
	if o.steps[claim.ID] != nil {
		o.mu.Unlock()
		return nil
	}
	o.steps[claim.ID] = execution
	o.mu.Unlock()
	return step
}
func (o *executionOwner) observe(ctx context.Context, execution *ownerExecution, step *ownerStep) {
	if step.current.Outcome != "accepted" {
		return
	}
	op, e := o.api.ObserveOperation(ctx, step.op)
	if e != nil {
		step.current.Outcome = "unknown"
		return
	}
	step.op = op
	if op.Outcome() == "succeeded" {
		id, e := o.api.ObserveResource(ctx, execution.prepared, step.claim.Step == admission.NativeStart)
		if e != nil || (step.claim.Step == admission.NativeStart && id != execution.quota.ResourceID()) {
			step.current.Outcome = "unknown"
			return
		}
		step.current.ResourceID = id
		if step.claim.Step == admission.NativeStart {
			guest, err := o.api.ObserveGuestIncarnation(ctx, execution.prepared, id)
			if err != nil {
				step.current.Outcome = "unknown"
				return
			}
			execution.guest = guest
		}
	}
	step.current.Outcome = op.Outcome()
}
func (o *executionOwner) start(ctx context.Context, permit string) (string, error) {
	if !permitID(permit) {
		return "", ErrControl
	}
	o.mu.Lock()
	execution := o.permits[permit]
	o.mu.Unlock()
	if execution == nil {
		return "", ErrControl
	}
	execution.mu.Lock()
	defer execution.mu.Unlock()
	if execution.startUsed {
		return "", ErrControl
	}
	execution.startUsed = true
	if execution.create == nil || execution.create.current.Outcome != "succeeded" || execution.bootstrapRecord.Outcome != "verified" || !execution.quota.Valid() {
		return "", ErrControl
	}
	q, e := o.api.VerifyStoppedQuota(ctx, execution.prepared, o.quota)
	if e != nil || q.ResourceID() != execution.quota.ResourceID() || q.DatasetGUID() != execution.quota.DatasetGUID() || q.Dataset() != execution.quota.Dataset() {
		return "", ErrControl
	}
	lifetime, e := o.api.CaptureDatasetLifetime(ctx, execution.prepared, q, o.quota)
	if e != nil {
		return "", ErrControl
	}
	execution.datasetLife = lifetime
	// Capture once before START. Failure consumes startUsed and cannot establish
	// a later baseline after the workload may have created retained references.
	if o.pools == nil {
		return "", ErrControl
	}
	reclamation, e := o.api.CaptureStorageReclamation(ctx, execution.prepared, q, o.quota, o.pools)
	if e != nil {
		return "", ErrControl
	}
	execution.reclamation = reclamation
	execution.reclamationCaptured = true
	claim, e := o.claims.step(ctx, execution.parent, admission.NativeStart)
	if e != nil {
		return "", e
	}
	step := o.installStep(execution, claim)
	if step == nil {
		return claim.ID, ErrControl
	}
	execution.start = step
	op, e := o.api.Start(ctx, execution.prepared, q)
	if e != nil {
		return claim.ID, ErrControl
	}
	step.op = op
	step.accepted.OperationID = op.ID()
	step.accepted.ResourceID = q.ResourceID()
	step.accepted.Outcome = "accepted"
	step.current = step.accepted
	return claim.ID, nil
}
