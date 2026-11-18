package nativecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
)

const CreateClaimPath = "/v1/native/claims/create-stopped"
const StartClaimPath = "/v1/native/claims/start"

type stepRequest struct {
	PermitID       string `json:"permit_id"`
	EnvelopeDigest string `json:"envelope_digest"`
	ParentClaimID  string `json:"parent_claim_id"`
}

func serveStep(w http.ResponseWriter, r *http.Request, s *admission.SQLiteStore, auth admission.NativeOwnerAuthority) bool {
	if r.URL.Path != CreateClaimPath && r.URL.Path != StartClaimPath {
		return false
	}
	if r.URL.RawQuery != "" || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "canonical POST required", 400)
		return true
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, 4097))
	var in stepRequest
	if e != nil || len(raw) > 4096 || decodeCanonical(raw, &in) != nil || !permitID(in.PermitID) || !hash(in.EnvelopeDigest) || !hash(in.ParentClaimID) {
		http.Error(w, "invalid step", 400)
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var claim admission.NativeOperationClaim
	if r.URL.Path == CreateClaimPath {
		claim, e = s.ClaimNativeCreate(ctx, auth, in.PermitID, in.EnvelopeDigest, in.ParentClaimID)
	} else {
		claim, e = s.ClaimNativeStart(ctx, auth, in.PermitID, in.EnvelopeDigest, in.ParentClaimID)
	}
	if e != nil {
		http.Error(w, "step unavailable; retain charge", 409)
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(201)
	_ = json.NewEncoder(w).Encode(claim)
	return true
}
func (c *ClaimClient) step(ctx context.Context, parent admission.FreshNativeClaim, step string) (admission.NativeOperationClaim, error) {
	fail := func() (admission.NativeOperationClaim, error) { return admission.NativeOperationClaim{}, ErrControl }
	if c == nil || c.client == nil || !hash(parent.ClaimID) || parent.Envelope.DomainID != c.config.DomainID || parent.Envelope.PolicyDigest != c.config.PolicyDigest {
		return fail()
	}
	digest, e := admission.NativeEnvelopeDigest(parent.Envelope)
	if e != nil || digest != parent.EnvelopeDigest {
		return fail()
	}
	path, want := CreateClaimPath, parent.Envelope.BodyDigest
	if step == admission.NativeStart {
		path, want = StartClaimPath, nativeidentity.NativeStartDigest()
	} else if step != admission.NativeCreateStopped {
		return fail()
	}
	raw, _ := json.Marshal(stepRequest{parent.Envelope.PermitID, digest, parent.ClaimID})
	r, e := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.endpoint, ClaimPath)+path, bytes.NewReader(raw))
	if e != nil {
		return fail()
	}
	r.Header.Set("Content-Type", "application/json")
	began := time.Now().UTC()
	response, e := c.client.Do(r)
	if e != nil {
		return fail()
	}
	defer response.Body.Close()
	if response.StatusCode != 201 || !peer(response.TLS, c.config.ControllerServerLeaf) {
		return fail()
	}
	raw, e = io.ReadAll(io.LimitReader(response.Body, 8193))
	if e != nil || len(raw) > 8192 {
		return fail()
	}
	var out admission.NativeOperationClaim
	if decodeCanonical(raw, &out) != nil || !hash(out.ID) || out.ParentClaimID != parent.ClaimID || out.PermitID != parent.Envelope.PermitID || out.EnvelopeDigest != digest || out.Step != step || out.RequestDigest != want || out.ClaimedAt.Before(began.Add(-30*time.Second)) || out.ClaimedAt.After(time.Now().Add(30*time.Second)) {
		return fail()
	}
	return out, nil
}
