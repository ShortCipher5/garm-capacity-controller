package nativecontrol

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBootstrapClaimExactFirstResponseNoRetry(t *testing.T) {
	for _, mode := range []string{"good", "lost", "changed", "stale", "status"} {
		t.Run(mode, func(t *testing.T) {
			plan := admission.NativeBootstrapPlan{PermitID: strings.Repeat("a", 32), EnvelopeDigest: strings.Repeat("b", 64), ParentClaimID: strings.Repeat("c", 64), DomainID: strings.Repeat("d", 64), PolicyDigest: strings.Repeat("e", 64)}
			raw, _ := json.Marshal(plan)
			var calls atomic.Int32
			server, _, cert := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != BootstrapClaimPath {
					t.Error(r.URL.Path)
				}
				if mode == "lost" {
					c, _, _ := w.(http.Hijacker).Hijack()
					c.Close()
					return
				}
				claim := admission.NativeBootstrapClaim{ID: strings.Repeat("f", 64), Plan: plan, PlanDigest: protectedboot.Hash(raw), ClaimedAt: time.Now().UTC()}
				if mode == "changed" {
					claim.Plan.ResourceID = "changed"
				}
				if mode == "stale" {
					claim.ClaimedAt = claim.ClaimedAt.Add(-time.Hour)
				}
				status := 201
				if mode == "status" {
					status = 200
				}
				w.WriteHeader(status)
				json.NewEncoder(w).Encode(claim)
			}))
			client, e := NewClaimClient(ClaimConfiguration{Endpoint: server.URL, TLS: cert, ControllerServerLeaf: nativeidentity.Fingerprint(server.TLS.Certificates[0].Certificate[0]), OwnerClientLeaf: fingerprint(cert), DomainID: plan.DomainID, PolicyDigest: plan.PolicyDigest})
			if e != nil {
				t.Fatal(e)
			}
			defer client.Close()
			_, e = client.bootstrap(t.Context(), plan)
			if (mode == "good") != (e == nil) {
				t.Fatal(mode, e)
			}
			if calls.Load() != 1 {
				t.Fatal("retried", calls.Load())
			}
		})
	}
}
func TestBootstrapAttemptConsumedEvenBeforePreconditionFailure(t *testing.T) {
	permit := strings.Repeat("a", 32)
	execution := &ownerExecution{}
	owner := &executionOwner{permits: map[string]*ownerExecution{permit: execution}}
	if _, e := owner.bootstrap(t.Context(), permit); e == nil || !execution.bootstrapUsed {
		t.Fatal("unsafe progression")
	}
	if _, e := owner.bootstrap(t.Context(), permit); e == nil {
		t.Fatal("retry")
	}
}
