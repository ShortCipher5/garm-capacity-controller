package nativecontrol

import (
	"crypto/sha256"
	"encoding/hex"
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

func TestRetirementClientExactFreshOneExchange(t *testing.T) {
	for _, step := range []string{admission.NativeRetireStop, admission.NativeRetireDelete} {
		for _, mode := range []string{"good", "lost", "changed", "stale", "cached"} {
			t.Run(step+"/"+mode, func(t *testing.T) {
				intent := admission.NativeRetirementIntent{Version: "native-retirement/v1", PermitID: strings.Repeat("a", 32), DomainID: strings.Repeat("b", 64), PolicyDigest: strings.Repeat("c", 64), NativeName: "runner-a", ResourceID: "11111111-1111-4111-8111-111111111111"}
				raw, _ := json.Marshal(intent)
				sum := sha256.Sum256(raw)
				digest := hex.EncodeToString(sum[:])
				requestDigest, e := admission.NativeRetirementRequestDigest(intent, step)
				if e != nil {
					t.Fatal(e)
				}
				var calls atomic.Int32
				server, _, cert := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					want := RetireStopClaimPath
					if step == admission.NativeRetireDelete {
						want = RetireDeleteClaimPath
					}
					if r.URL.Path != want || r.Method != http.MethodPost {
						t.Error("wrong command route")
					}
					var in retirementRequest
					if e := json.NewDecoder(r.Body).Decode(&in); e != nil || in.PermitID != intent.PermitID || in.IntentDigest != digest {
						t.Error("request intent drift", e)
					}
					if mode == "lost" {
						conn, _, _ := w.(http.Hijacker).Hijack()
						conn.Close()
						return
					}
					out := admission.NativeRetirementClaim{ID: strings.Repeat("d", 64), PermitID: intent.PermitID, IntentDigest: digest, Step: step, RequestDigest: requestDigest, ClaimedAt: time.Now().UTC()}
					if mode == "changed" {
						out.RequestDigest = strings.Repeat("e", 64)
					}
					if mode == "stale" {
						out.ClaimedAt = out.ClaimedAt.Add(-time.Hour)
					}
					status := 201
					if mode == "cached" {
						status = 200
					}
					w.WriteHeader(status)
					_ = json.NewEncoder(w).Encode(out)
				}))
				client, e := NewClaimClient(ClaimConfiguration{Endpoint: server.URL, TLS: cert, ControllerServerLeaf: nativeidentity.Fingerprint(server.TLS.Certificates[0].Certificate[0]), OwnerClientLeaf: fingerprint(cert), DomainID: intent.DomainID, PolicyDigest: intent.PolicyDigest})
				if e != nil {
					t.Fatal(e)
				}
				defer client.Close()
				out, e := client.retirement(t.Context(), intent, step)
				if (mode == "good") != (e == nil) {
					t.Fatal(mode, e)
				}
				if e != nil && out != (admission.NativeRetirementClaim{}) {
					t.Fatal("invalid detached claim")
				}
				if calls.Load() != 1 {
					t.Fatal("retried", calls.Load())
				}
			})
		}
	}
}
