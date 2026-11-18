//go:build linux

package offlinestack

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecontroller"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
)

// This owner is a protocol double, not evidence of actual STOP/DELETE or absence.
func (o *nativeBridgeOwner) serveRetirement(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/retire-") {
		step := r.URL.Path[strings.LastIndex(r.URL.Path, "/retire-")+8:]
		if o.retirementCalls == nil {
			o.retirementCalls = map[string]int{}
			o.retirementClaims = map[string]admission.NativeRetirementClaim{}
		}
		o.retirementCalls[step]++
		var supplied admission.NativeRetirementIntent
		if json.NewDecoder(r.Body).Decode(&supplied) != nil {
			w.WriteHeader(400)
			return true
		}
		intent, digest, e := o.store.LookupNativeRetirement(r.Context(), o.parent.Envelope.PermitID)
		if e != nil || supplied != intent {
			o.t.Error("wrong retirement intent", e)
			w.WriteHeader(409)
			return true
		}
		claim, e := o.store.ClaimNativeRetirement(r.Context(), o.authority, intent.PermitID, digest, step)
		if e != nil {
			o.t.Error("retirement claim", e)
			w.WriteHeader(409)
			return true
		}
		o.retirementClaims[step] = claim
		if o.retirementCancel != nil {
			cancel := o.retirementCancel
			o.retirementCancel = nil
			cancel()
			<-r.Context().Done()
			return true
		}
		if o.mode == "retire-"+step+"-ack-lost" {
			conn, _, e := w.(http.Hijacker).Hijack()
			if e != nil {
				o.t.Error(e)
			} else {
				conn.Close()
			}
			return true
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]string{"retirement_claim_id": claim.ID})
		return true
	}
	prefix := "/v1/native/retirement-operations/"
	if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, prefix) {
		return false
	}
	id := strings.TrimPrefix(r.URL.Path, prefix)
	terminal := strings.HasSuffix(id, "/terminal")
	id = strings.TrimSuffix(id, "/terminal")
	var claim admission.NativeRetirementClaim
	for _, v := range o.retirementClaims {
		if v.ID == id {
			claim = v
		}
	}
	if claim.ID == "" {
		w.WriteHeader(404)
		return true
	}
	intent, _, e := o.store.LookupNativeRetirement(r.Context(), claim.PermitID)
	if e != nil {
		o.t.Error(e)
		w.WriteHeader(500)
		return true
	}
	outcome := "accepted"
	if terminal {
		outcome = "succeeded"
		if o.mode == "retire-"+claim.Step+"-unknown" {
			outcome = "unknown"
		}
	}
	op := "44444444-4444-4444-8444-444444444444"
	if claim.Step == admission.NativeRetireDelete {
		op = "55555555-5555-4555-8555-555555555555"
	}
	json.NewEncoder(w).Encode(nativeidentity.RetirementOperationRecord{PermitID: claim.PermitID, IntentDigest: claim.IntentDigest, ClaimID: claim.ID, RequestDigest: claim.RequestDigest, DomainID: intent.DomainID, PolicyDigest: intent.PolicyDigest, NativeName: intent.NativeName, ResourceID: intent.ResourceID, Dataset: intent.Dataset, DatasetGUID: intent.DatasetGUID, Step: claim.Step, OperationID: op, Outcome: outcome})
	return true
}

func exerciseRetirement(t *testing.T, f *nativeBridgeFixture, permit string, reopen func()) {
	t.Helper()
	f.config.TerminalTimeout = 10 * time.Second
	if f.owner.mode == "retire-stop-cancel-restart" {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.owner.mu.Lock()
		f.owner.retirementCancel = cancel
		f.owner.mu.Unlock()
		c, e := nativecontroller.New(context.Background(), f.config)
		if e != nil {
			t.Fatal(e)
		}
		result, e := c.Retire(ctx, permit)
		if e == nil || result.ReleaseAuthorized {
			t.Fatal("cancelled mutation acknowledged", result, e)
		}
		reopen()
	}
	run := func() {
		c, e := nativecontroller.New(context.Background(), f.config)
		if e != nil {
			t.Fatal(e)
		}
		result, e := c.Retire(context.Background(), permit)
		if result.ReleaseAuthorized {
			t.Fatal("retirement inferred physical release")
		}
		unknown := strings.HasSuffix(f.owner.mode, "-unknown")
		if unknown && e == nil || !unknown && e != nil {
			t.Fatalf("retirement result %+v %v", result, e)
		}
		if f.owner.mode == "retire-stop-unknown" && result.StopState != "unknown" {
			t.Fatal(result)
		}
		if f.owner.mode == "retire-delete-unknown" && (result.StopState != "succeeded" || result.DeleteState != "unknown") {
			t.Fatal(result)
		}
		if !unknown && (result.StopState != "succeeded" || result.DeleteState != "succeeded") {
			t.Fatal(result)
		}
	}
	run()
	intent, _, e := f.s.LookupNativeRetirement(context.Background(), permit)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.LookupRelease(context.Background(), intent.RequestKey); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal("unexpected release", e)
	}
	reopen()
	run()
	f.owner.mu.Lock()
	defer f.owner.mu.Unlock()
	wantDelete := 1
	if f.owner.mode == "retire-stop-unknown" {
		wantDelete = 0
	}
	if f.owner.retirementCalls[admission.NativeRetireStop] != 1 || f.owner.retirementCalls[admission.NativeRetireDelete] != wantDelete {
		t.Fatalf("retirement replayed or unsafe DELETE: %v", f.owner.retirementCalls)
	}
}
