package nativecontrol

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

func claimEnvelope() admission.NativeExecutionEnvelope {
	return admission.NativeExecutionEnvelope{Version: "native-execution/v1", Operation: "create_and_start", PermitID: strings.Repeat("a", 32), DomainID: strings.Repeat("b", 64), PolicyDigest: strings.Repeat("c", 64), BodyDigest: strings.Repeat("d", 64)}
}
func TestClaimClientRequiresFreshPinned201AndDoesNotRetry(t *testing.T) {
	envelope := claimEnvelope()
	digest, _ := admission.NativeEnvelopeDigest(envelope)
	var calls atomic.Int32
	server, _, owner := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ClaimPath || r.Method != http.MethodPost {
			t.Error("wrong claim route")
		}
		if calls.Add(1) > 1 {
			http.Error(w, "already consumed", 409)
			return
		}
		var request claimRequest
		json.NewDecoder(r.Body).Decode(&request)
		if request.PermitID != envelope.PermitID || request.EnvelopeDigest != digest {
			t.Error("changed canonical tuple")
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(admission.FreshNativeClaim{ClaimID: strings.Repeat("e", 64), EnvelopeDigest: digest, Envelope: envelope, ConsumedAt: time.Now().UTC()})
	}))
	client, e := NewClaimClient(ClaimConfiguration{Endpoint: server.URL, TLS: owner, ControllerServerLeaf: nativeidentity.Fingerprint(server.TLS.Certificates[0].Certificate[0]), OwnerClientLeaf: fingerprint(owner), DomainID: envelope.DomainID, PolicyDigest: envelope.PolicyDigest})
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	if _, e = client.consume(context.Background(), envelope); e != nil {
		t.Fatal("fresh claim rejected", e)
	}
	if _, e = client.consume(context.Background(), envelope); e != ErrControl {
		t.Fatal("replayed authority accepted", e)
	}
	if calls.Load() != 2 {
		t.Fatal("implicit retry", calls.Load())
	}
}
func TestClaimClientRejectsLostOrDetachedChangedResponses(t *testing.T) {
	for _, mode := range []string{"lost", "old-status", "changed", "duplicate", "stale"} {
		t.Run(mode, func(t *testing.T) {
			envelope := claimEnvelope()
			digest, _ := admission.NativeEnvelopeDigest(envelope)
			var calls atomic.Int32
			server, _, owner := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if mode == "lost" {
					conn, _, e := w.(http.Hijacker).Hijack()
					if e == nil {
						conn.Close()
					}
					return
				}
				claim := admission.FreshNativeClaim{ClaimID: strings.Repeat("e", 64), EnvelopeDigest: digest, Envelope: envelope, ConsumedAt: time.Now().UTC()}
				if mode == "changed" {
					claim.Envelope.DomainID = strings.Repeat("f", 64)
				}
				if mode == "stale" {
					claim.ConsumedAt = claim.ConsumedAt.Add(-time.Hour)
				}
				if mode == "old-status" {
					w.WriteHeader(200)
				} else {
					w.WriteHeader(201)
				}
				raw, _ := json.Marshal(claim)
				if mode == "duplicate" {
					raw = append([]byte(`{"claim_id":"`+claim.ClaimID+`",`), raw[1:]...)
				}
				w.Write(raw)
			}))
			client, e := NewClaimClient(ClaimConfiguration{Endpoint: server.URL, TLS: owner, ControllerServerLeaf: nativeidentity.Fingerprint(server.TLS.Certificates[0].Certificate[0]), OwnerClientLeaf: fingerprint(owner), DomainID: envelope.DomainID, PolicyDigest: envelope.PolicyDigest})
			if e != nil {
				t.Fatal(e)
			}
			defer client.Close()
			if _, e = client.consume(context.Background(), envelope); e != ErrControl {
				t.Fatal("unsafe response accepted", e)
			}
			if calls.Load() != 1 {
				t.Fatal("retry or failed handshake", calls.Load())
			}
		})
	}
}
func TestClaimConfigurationRejectsCrossRoleAndMissingPins(t *testing.T) {
	server, controller, owner := testsupport.NativeTLS(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") }))
	config := ClaimConfiguration{Endpoint: server.URL, TLS: owner, ControllerServerLeaf: nativeidentity.Fingerprint(server.TLS.Certificates[0].Certificate[0]), OwnerClientLeaf: fingerprint(owner), DomainID: strings.Repeat("b", 64), PolicyDigest: strings.Repeat("c", 64)}
	wrong := config
	wrong.TLS = controller
	if _, e := NewClaimClient(wrong); e != ErrControl {
		t.Fatal("cross-role cert accepted")
	}
	wrong = config
	wrong.ControllerServerLeaf = ""
	if _, e := NewClaimClient(wrong); e != ErrControl {
		t.Fatal("missing pin accepted")
	}
	wrong = config
	wrong.Endpoint += "/injected"
	if _, e := NewClaimClient(wrong); e != ErrControl {
		t.Fatal("caller route accepted")
	}
}
func TestCanonicalDecodeRejectsCaseDuplicatesAndUnknownFields(t *testing.T) {
	for _, raw := range []string{`{"Permit_ID":"a","envelope_digest":"b"}`, `{"permit_id":"a","permit_id":"a","envelope_digest":"b"}`, `{"permit_id":"a","envelope_digest":"b","claimed":true}`} {
		var request claimRequest
		if decodeCanonical([]byte(raw), &request) == nil {
			t.Fatal("ambiguous JSON", raw)
		}
	}
}
