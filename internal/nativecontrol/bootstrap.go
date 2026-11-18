package nativecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
	"io"
	"net/http"
	"strings"
	"time"
)

const BootstrapClaimPath = "/v1/native/claims/bootstrap"

func serveBootstrap(w http.ResponseWriter, r *http.Request, s *admission.SQLiteStore, auth admission.NativeOwnerAuthority) bool {
	if r.URL.Path != BootstrapClaimPath {
		return false
	}
	if r.URL.RawQuery != "" || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "canonical POST required", 400)
		return true
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, 4097))
	var in stepRequest
	if e != nil || len(raw) > 4096 || decodeCanonical(raw, &in) != nil || !permitID(in.PermitID) || !hash(in.EnvelopeDigest) || !hash(in.ParentClaimID) {
		http.Error(w, "invalid bootstrap claim", 400)
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	claim, e := s.ClaimNativeBootstrap(ctx, auth, in.PermitID, in.EnvelopeDigest, in.ParentClaimID)
	if e != nil {
		http.Error(w, "bootstrap unavailable; no retry", 409)
		return true
	}
	respond(w, 201, claim)
	return true
}
func (c *ClaimClient) bootstrap(ctx context.Context, plan admission.NativeBootstrapPlan) (admission.NativeBootstrapClaim, error) {
	fail := func() (admission.NativeBootstrapClaim, error) { return admission.NativeBootstrapClaim{}, ErrControl }
	if c == nil || c.client == nil || plan.DomainID != c.config.DomainID || plan.PolicyDigest != c.config.PolicyDigest || !permitID(plan.PermitID) || !hash(plan.EnvelopeDigest) || !hash(plan.ParentClaimID) {
		return fail()
	}
	raw, _ := json.Marshal(stepRequest{plan.PermitID, plan.EnvelopeDigest, plan.ParentClaimID})
	r, e := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.endpoint, ClaimPath)+BootstrapClaimPath, bytes.NewReader(raw))
	if e != nil {
		return fail()
	}
	r.Header.Set("Content-Type", "application/json")
	began := time.Now()
	response, e := c.client.Do(r)
	if e != nil {
		return fail()
	}
	defer response.Body.Close()
	if response.StatusCode != 201 || !peer(response.TLS, c.config.ControllerServerLeaf) {
		return fail()
	}
	raw, e = io.ReadAll(io.LimitReader(response.Body, 16385))
	if e != nil || len(raw) > 16384 {
		return fail()
	}
	var out admission.NativeBootstrapClaim
	planRaw, _ := json.Marshal(plan)
	if decodeCanonical(raw, &out) != nil || !hash(out.ID) || out.Plan != plan || out.PlanDigest != protectedboot.Hash(planRaw) || out.ClaimedAt.Before(began.Add(-30*time.Second)) || out.ClaimedAt.After(time.Now().Add(30*time.Second)) {
		return fail()
	}
	return out, nil
}
func (o *executionOwner) bootstrap(ctx context.Context, permit string) (string, error) {
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
	if execution.bootstrapUsed {
		return "", ErrControl
	}
	execution.bootstrapUsed = true
	if execution.create == nil || execution.create.current.Outcome != "succeeded" {
		return "", ErrControl
	}
	payload, e := protectedboot.Decode([]byte(execution.userData))
	if e != nil {
		return "", ErrControl
	}
	parent := execution.parent
	resource := execution.create.current.ResourceID
	files, e := protectedboot.Materialize(payload, protectedboot.Binding{DomainID: parent.Envelope.DomainID, PolicyDigest: parent.Envelope.PolicyDigest, ResourceID: resource, ImageFingerprint: parent.Envelope.Execution.Fingerprint})
	if e != nil {
		return "", ErrControl
	}
	plan := admission.NativeBootstrapPlan{PermitID: permit, EnvelopeDigest: parent.EnvelopeDigest, ParentClaimID: parent.ClaimID, CreateStepClaimID: execution.create.claim.ID, CreateOperationID: execution.create.op.ID(), DomainID: parent.Envelope.DomainID, PolicyDigest: parent.Envelope.PolicyDigest, NativeName: parent.Envelope.NativeName, ResourceID: resource, ImageFingerprint: parent.Envelope.Execution.Fingerprint, LaunchSHA256: files[0].SHA256, LaunchBytes: files[0].Bytes, JITSHA256: files[1].SHA256, JITBytes: files[1].Bytes}
	claim, e := o.claims.bootstrap(ctx, plan)
	if e != nil {
		return "", e
	}
	execution.bootstrapClaim = claim
	execution.bootstrapRecord = nativeidentity.BootstrapRecord{ClaimID: claim.ID, PlanDigest: claim.PlanDigest, PermitID: permit, EnvelopeDigest: plan.EnvelopeDigest, ParentClaimID: plan.ParentClaimID, DomainID: plan.DomainID, PolicyDigest: plan.PolicyDigest, NativeName: plan.NativeName, ResourceID: resource, ImageFingerprint: plan.ImageFingerprint, Outcome: "unknown", LaunchSHA256: plan.LaunchSHA256, LaunchBytes: plan.LaunchBytes, JITSHA256: plan.JITSHA256, JITBytes: plan.JITBytes}
	o.mu.Lock()
	if o.steps[claim.ID] != nil {
		o.mu.Unlock()
		return claim.ID, ErrControl
	}
	o.steps[claim.ID] = execution
	o.mu.Unlock()
	observed, e := o.api.DeliverBootstrap(ctx, execution.prepared, resource)
	if e != nil || !observed.Valid() || observed.ResourceID() != resource {
		return claim.ID, ErrControl
	}
	actual := observed.Digests()
	if actual[0].SHA256 != plan.LaunchSHA256 || actual[0].Bytes != plan.LaunchBytes || actual[1].SHA256 != plan.JITSHA256 || actual[1].Bytes != plan.JITBytes {
		return claim.ID, ErrControl
	}
	// These metadata values are constants only because the opaque native adapter
	// validated actual headers for both files. No request can fabricate a receipt.
	record := &execution.bootstrapRecord
	record.Outcome = "verified"
	record.LaunchMode = "0600"
	record.LaunchType = "file"
	record.JITMode = "0600"
	record.JITType = "file"
	return claim.ID, nil
}
