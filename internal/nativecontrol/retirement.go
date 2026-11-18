package nativecontrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

const RetireStopClaimPath = "/v1/native/claims/retire-stop"
const RetireDeleteClaimPath = "/v1/native/claims/retire-delete"

type retirementRequest struct {
	PermitID     string `json:"permit_id"`
	IntentDigest string `json:"intent_digest"`
}

func serveRetirement(w http.ResponseWriter, r *http.Request, s *admission.SQLiteStore, auth admission.NativeOwnerAuthority) bool {
	step := admission.NativeRetireStop
	switch r.URL.Path {
	case RetireStopClaimPath:
	case RetireDeleteClaimPath:
		step = admission.NativeRetireDelete
	default:
		return false
	}
	if r.URL.RawQuery != "" || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "canonical POST required", 400)
		return true
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, 4097))
	var in retirementRequest
	if e != nil || len(raw) > 4096 || decodeCanonical(raw, &in) != nil || !permitID(in.PermitID) || !hash(in.IntentDigest) {
		http.Error(w, "invalid retirement claim", 400)
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	claim, e := s.ClaimNativeRetirement(ctx, auth, in.PermitID, in.IntentDigest, step)
	if e != nil {
		http.Error(w, "retirement unavailable; retain charge", 409)
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(claim)
	return true
}

// retirement remains private to the owner package. It performs exactly one fresh
// authenticated exchange and never retries a lost ACK or returns a cached claim.
// The ledger's runtime refusal remains mandatory, independent of this transport.
func (c *ClaimClient) retirement(ctx context.Context, intent admission.NativeRetirementIntent, step string) (admission.NativeRetirementClaim, error) {
	fail := func() (admission.NativeRetirementClaim, error) { return admission.NativeRetirementClaim{}, ErrControl }
	if c == nil || c.client == nil || intent.DomainID != c.config.DomainID || intent.PolicyDigest != c.config.PolicyDigest || !permitID(intent.PermitID) {
		return fail()
	}
	requestDigest, e := admission.NativeRetirementRequestDigest(intent, step)
	if e != nil {
		return fail()
	}
	path := RetireStopClaimPath
	if step == admission.NativeRetireDelete {
		path = RetireDeleteClaimPath
	} else if step != admission.NativeRetireStop {
		return fail()
	}
	intentRaw, e := json.Marshal(intent)
	if e != nil {
		return fail()
	}
	sum := sha256.Sum256(intentRaw)
	digest := hex.EncodeToString(sum[:])
	raw, _ := json.Marshal(retirementRequest{intent.PermitID, digest})
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
	if response.StatusCode != http.StatusCreated || !peer(response.TLS, c.config.ControllerServerLeaf) {
		return fail()
	}
	raw, e = io.ReadAll(io.LimitReader(response.Body, 8193))
	if e != nil || len(raw) > 8192 {
		return fail()
	}
	var out admission.NativeRetirementClaim
	if decodeCanonical(raw, &out) != nil || !hash(out.ID) || out.PermitID != intent.PermitID || out.IntentDigest != digest || out.Step != step || out.RequestDigest != requestDigest || out.ClaimedAt.Before(began.Add(-30*time.Second)) || out.ClaimedAt.After(time.Now().Add(30*time.Second)) {
		return fail()
	}
	return out, nil
}
