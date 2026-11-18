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
		service, e := nativecontroller.NewRetirementService(context.Background(), nativecontroller.RetirementConfiguration{Store: f.s, Owners: []nativecontroller.Configuration{f.config}, Interval: time.Second})
		if e != nil {
			t.Fatal(e)
		}
		cycle, e := service.Cycle(context.Background())
		if e != nil || cycle.Examined != 1 || cycle.ReleaseAuthorized {
			t.Fatal("automation cycle", cycle, e)
		}
		// A bounded cycle can stop after a durable claim without terminal proof.
		// Recover only that existing history; a permanent unknown/failed receipt
		// is never eligible for this eventual-success assertion.
		for attempt := 0; attempt < 3 && !strings.HasSuffix(f.owner.mode, "-unknown") && cycle.OperationsCompleted == 0; attempt++ {
			found := false
			for _, step := range []string{admission.NativeRetireStop, admission.NativeRetireDelete} {
				claim, err := f.s.LookupNativeRetirementClaim(context.Background(), permit, step)
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				if err != nil {
					t.Fatal("cannot recover uncertain canonical claim", err)
				}
				found = true
				for _, kind := range []string{"failed", "unknown"} {
					if _, err := f.s.LookupNativeRetirementReceipt(context.Background(), claim.ID, kind); !errors.Is(err, sql.ErrNoRows) {
						t.Fatal("irreversible receipt cannot be retried", kind, err)
					}
				}
			}
			if !found {
				t.Fatal("unresolved automation without durable claim", cycle)
			}
			intent, _, err := f.s.LookupNativeRetirement(context.Background(), permit)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.LookupRelease(context.Background(), intent.RequestKey); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("recovery released charge", err)
			}
			f.owner.mu.Lock()
			excess := f.owner.retirementCalls[admission.NativeRetireStop] > 1 || f.owner.retirementCalls[admission.NativeRetireDelete] > 1
			f.owner.mu.Unlock()
			if excess {
				t.Fatal("recovery already replayed a native POST")
			}
			cycle, e = service.Cycle(context.Background())
			if e != nil || cycle.ReleaseAuthorized {
				t.Fatal("recovery cycle", cycle, e)
			}
		}
		if strings.HasSuffix(f.owner.mode, "-unknown") {
			if cycle.Unresolved != 1 || cycle.OperationsCompleted != 0 {
				t.Fatal("unknown automation", cycle)
			}
		} else if cycle.OperationsCompleted != 1 || cycle.Unresolved != 0 {
			for _, step := range []string{admission.NativeRetireStop, admission.NativeRetireDelete} {
				claim, err := f.s.LookupNativeRetirementClaim(context.Background(), permit, step)
				t.Logf("diagnostic step=%s claim=%s error=%v", step, claim.ID, err)
				if err == nil {
					for _, kind := range []string{"accepted", "succeeded", "failed", "unknown"} {
						receipt, err := f.s.LookupNativeRetirementReceipt(context.Background(), claim.ID, kind)
						t.Logf("diagnostic step=%s kind=%s outcome=%s error=%v", step, kind, receipt.Record.Outcome, err)
					}
				}
			}
			t.Fatal("completed automation", cycle)
		}
		// Existing direct coordinator assertions below must now recover only durable
		// history, not issue a second STOP/DELETE after the real service cycle.
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
