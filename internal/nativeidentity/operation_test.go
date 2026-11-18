package nativeidentity_test

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestNativeOperationAndQuotaEvidenceRequireActualPinnedReads(t *testing.T) {
	var descriptor nativeidentity.Descriptor
	var mu sync.Mutex
	mode := ""
	id := strings.Repeat("d", 64)
	domain := strings.Repeat("a", 64)
	server, controller, owner := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == nativeidentity.Path {
			json.NewEncoder(w).Encode(descriptor)
			return
		}
		mu.Lock()
		m := mode
		mu.Unlock()
		if m == "redirect" {
			http.Redirect(w, r, "https://elsewhere.invalid", 302)
			return
		}
		var v any
		if strings.HasPrefix(r.URL.Path, "/v1/native/operations/") {
			outcome := "accepted"
			resource := ""
			if strings.HasSuffix(r.URL.Path, "/terminal") {
				outcome = "succeeded"
				resource = "33333333-3333-4333-8333-333333333333"
			}
			if m == "wrong-phase" {
				outcome = "succeeded"
				resource = "33333333-3333-4333-8333-333333333333"
			}
			record := nativeidentity.OperationRecord{PermitID: "permit", EnvelopeDigest: strings.Repeat("e", 64), ParentClaimID: strings.Repeat("f", 64), Step: "create_stopped", StepClaimID: id, DomainID: domain, PolicyDigest: descriptor.PolicyDigest, NativeName: "runner", OperationID: "11111111-1111-4111-8111-111111111111", Outcome: outcome, ResourceID: resource}
			if m == "wrong-claim" {
				record.StepClaimID = strings.Repeat("0", 64)
			}
			v = record
		} else {
			record := nativeidentity.QuotaRecord{PermitID: "permit", EnvelopeDigest: strings.Repeat("e", 64), ParentClaimID: strings.Repeat("f", 64), CreateStepClaimID: id, DomainID: domain, PolicyDigest: descriptor.PolicyDigest, NativeName: "runner", ResourceID: "33333333-3333-4333-8333-333333333333", CreateOperationID: "11111111-1111-4111-8111-111111111111", Backend: "zfs", Dataset: "owned/containers/runner", DatasetGUID: "12345", QuotaKind: "quota", RequestedBytes: 4096, EnforcedBytes: 4096, InstanceState: "stopped"}
			switch m {
			case "refquota":
				record.QuotaKind = "refquota"
			case "unlimited":
				record.EnforcedBytes = 0
			case "running":
				record.InstanceState = "running"
			case "guid":
				record.DatasetGUID = "0"
			}
			v = record
		}
		raw, _ := json.Marshal(v)
		if m == "duplicate" {
			raw = append([]byte(`{"permit_id":"duplicate",`), raw[1:]...)
		}
		w.Write(raw)
	}))
	descriptor = nativeidentity.Descriptor{DomainID: domain, PolicyDigest: strings.Repeat("b", 64), OwnerServerLeaf: nativeidentity.Fingerprint(server.TLS.Certificates[0].Certificate[0]), OwnerClientLeaf: nativeidentity.Fingerprint(owner.Certificates[0].Certificate[0]), NativeServerKey: strings.Repeat("c", 64), NativeOrigin: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain)), NativeScope: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain + "\x00default")), NativeServerName: "native-fixture", NativeServerVersion: "6.0.4"}
	cfg := nativeidentity.Configuration{Endpoint: server.URL, TLS: controller, OwnerServerLeaf: descriptor.OwnerServerLeaf, OwnerClientLeaf: descriptor.OwnerClientLeaf, PolicyDigest: descriptor.PolicyDigest, DomainID: domain}
	ctx := context.Background()
	if _, e := nativeidentity.ObserveOperation(ctx, cfg, id); e != nil {
		t.Fatal(e)
	}
	if _, e := nativeidentity.ObserveOperationTerminal(ctx, cfg, id); e != nil {
		t.Fatal(e)
	}
	if _, e := nativeidentity.ObserveQuota(ctx, cfg, id); e != nil {
		t.Fatal(e)
	}
	if (nativeidentity.OperationObservation{}).Valid() || (nativeidentity.QuotaObservation{}).Valid() {
		t.Fatal("zero proof promoted")
	}
	for _, bad := range []string{"redirect", "duplicate", "wrong-claim", "wrong-phase"} {
		mu.Lock()
		mode = bad
		mu.Unlock()
		if _, e := nativeidentity.ObserveOperation(ctx, cfg, id); e == nil {
			t.Fatal("operation accepted", bad)
		}
	}
	for _, bad := range []string{"redirect", "duplicate", "refquota", "unlimited", "running", "guid"} {
		mu.Lock()
		mode = bad
		mu.Unlock()
		if _, e := nativeidentity.ObserveQuota(ctx, cfg, id); e == nil {
			t.Fatal("quota accepted", bad)
		}
	}
}
