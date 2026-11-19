package nativecontrol

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStepClientFreshExactAndNoReplay(t *testing.T) {
	for _, mode := range []string{"good", "lost", "changed", "stale", "status"} {
		t.Run(mode, func(t *testing.T) {
			envelope := claimEnvelope()
			digest, _ := admission.NativeEnvelopeDigest(envelope)
			parent := admission.FreshNativeClaim{ClaimID: strings.Repeat("e", 64), Envelope: envelope, EnvelopeDigest: digest}
			var calls atomic.Int32
			server, _, cert := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != CreateClaimPath {
					t.Error(r.URL.Path)
				}
				if mode == "lost" {
					c, _, _ := w.(http.Hijacker).Hijack()
					c.Close()
					return
				}
				out := admission.NativeOperationClaim{ID: strings.Repeat("f", 64), ParentClaimID: parent.ClaimID, PermitID: envelope.PermitID, EnvelopeDigest: digest, Step: admission.NativeCreateStopped, RequestDigest: envelope.BodyDigest, ClaimedAt: time.Now().UTC()}
				if mode == "changed" {
					out.RequestDigest = strings.Repeat("1", 64)
				}
				if mode == "stale" {
					out.ClaimedAt = out.ClaimedAt.Add(-time.Hour)
				}
				status := 201
				if mode == "status" {
					status = 200
				}
				w.WriteHeader(status)
				json.NewEncoder(w).Encode(out)
			}))
			c, e := NewClaimClient(ClaimConfiguration{Endpoint: server.URL, TLS: cert, ControllerServerLeaf: nativeidentity.Fingerprint(server.TLS.Certificates[0].Certificate[0]), OwnerClientLeaf: fingerprint(cert), DomainID: envelope.DomainID, PolicyDigest: envelope.PolicyDigest})
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			_, e = c.step(t.Context(), parent, admission.NativeCreateStopped)
			if (mode == "good") != (e == nil) {
				t.Fatal(mode, e)
			}
			if calls.Load() != 1 {
				t.Fatal("retried", calls.Load())
			}
		})
	}
}
func TestOwnerLostParentACKPermanentlyConsumesNameAndPermit(t *testing.T) {
	p, envelope, data := prepareFixture(t)
	var calls atomic.Int32
	server, _, cert := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		c, _, _ := w.(http.Hijacker).Hijack()
		c.Close()
	}))
	claims, e := NewClaimClient(ClaimConfiguration{Endpoint: server.URL, TLS: cert, ControllerServerLeaf: nativeidentity.Fingerprint(server.TLS.Certificates[0].Certificate[0]), OwnerClientLeaf: fingerprint(cert), DomainID: envelope.DomainID, PolicyDigest: envelope.PolicyDigest})
	if e != nil {
		t.Fatal(e)
	}
	defer claims.Close()
	owner := &executionOwner{policy: p, claims: claims, permits: map[string]*ownerExecution{}, names: map[string]bool{}, steps: map[string]*ownerExecution{}}
	if _, e := owner.create(t.Context(), executionRequest{envelope, data}); e == nil {
		t.Fatal("lost ack accepted")
	}
	if _, e := owner.create(t.Context(), executionRequest{envelope, data}); e == nil {
		t.Fatal("replayed permit")
	}
	envelope.PermitID = strings.Repeat("2", 32)
	if _, e := owner.create(t.Context(), executionRequest{envelope, data}); e == nil {
		t.Fatal("replayed physical name")
	}
	if calls.Load() != 1 {
		t.Fatal("claim retried", calls.Load())
	}
	if len(owner.permits) != 1 || len(owner.names) != 1 {
		t.Fatal("lost tombstones")
	}
}
