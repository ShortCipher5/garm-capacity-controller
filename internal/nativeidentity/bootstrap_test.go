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

func TestBootstrapRequiresPinnedExactReadback(t *testing.T) {
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
		if r.URL.Path != "/v1/native/bootstraps/"+id {
			t.Error("wrong evidence path")
			w.WriteHeader(404)
			return
		}
		mu.Lock()
		m := mode
		mu.Unlock()
		record := nativeidentity.BootstrapRecord{ClaimID: id, PlanDigest: strings.Repeat("1", 64), PermitID: "permit", EnvelopeDigest: strings.Repeat("e", 64), ParentClaimID: strings.Repeat("f", 64), DomainID: domain, PolicyDigest: descriptor.PolicyDigest, NativeName: "runner", ResourceID: "11111111-1111-4111-8111-111111111111", ImageFingerprint: strings.Repeat("2", 64), Outcome: "verified", LaunchSHA256: strings.Repeat("3", 64), LaunchBytes: 100, JITSHA256: strings.Repeat("4", 64), JITBytes: 200, LaunchMode: "0600", LaunchType: "file", JITMode: "0600", JITType: "file"}
		switch m {
		case "uid":
			record.JITUID = 1001
		case "mode":
			record.LaunchMode = "0644"
		case "symlink":
			record.JITType = "symlink"
		case "claim":
			record.ClaimID = strings.Repeat("0", 64)
		case "domain":
			record.DomainID = strings.Repeat("0", 64)
		case "unknown":
			record.Outcome = "unknown"
		case "redirect":
			http.Redirect(w, r, "https://other.invalid", 302)
			return
		}
		raw, _ := json.Marshal(record)
		if m == "duplicate" {
			raw = append([]byte(`{"claim_id":"duplicate",`), raw[1:]...)
		}
		w.Write(raw)
	}))
	descriptor = nativeidentity.Descriptor{DomainID: domain, PolicyDigest: strings.Repeat("b", 64), OwnerServerLeaf: nativeidentity.Fingerprint(server.TLS.Certificates[0].Certificate[0]), OwnerClientLeaf: nativeidentity.Fingerprint(owner.Certificates[0].Certificate[0]), NativeServerKey: strings.Repeat("c", 64), NativeOrigin: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain)), NativeScope: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain + "\x00default")), NativeServerName: "fixture", NativeServerVersion: "6.0.4"}
	cfg := nativeidentity.Configuration{Endpoint: server.URL, TLS: controller, OwnerServerLeaf: descriptor.OwnerServerLeaf, OwnerClientLeaf: descriptor.OwnerClientLeaf, PolicyDigest: descriptor.PolicyDigest, DomainID: domain}
	if _, e := nativeidentity.ObserveBootstrap(context.Background(), cfg, id); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{"uid", "mode", "symlink", "claim", "domain", "duplicate", "redirect"} {
		mu.Lock()
		mode = bad
		mu.Unlock()
		if _, e := nativeidentity.ObserveBootstrap(context.Background(), cfg, id); e == nil {
			t.Fatal("unsafe evidence", bad)
		}
	}
	mu.Lock()
	mode = "unknown"
	mu.Unlock()
	o, e := nativeidentity.ObserveBootstrap(context.Background(), cfg, id)
	if e != nil || o.Record().Outcome != "unknown" {
		t.Fatal("unknown lost", e)
	}
	if (nativeidentity.BootstrapObservation{}).Valid() {
		t.Fatal("selfattested observation")
	}
}
