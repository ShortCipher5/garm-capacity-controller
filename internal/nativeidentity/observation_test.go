package nativeidentity_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

func TestLivePinnedObservationAndRejectedClaims(t *testing.T) {
	var descriptor nativeidentity.Descriptor
	var mode string
	server, controller, owner := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != nativeidentity.Path {
			t.Error("wrong endpoint")
		}
		switch mode {
		case "redirect":
			http.Redirect(w, r, "https://elsewhere.invalid", 302)
			return
		case "duplicate":
			raw, _ := json.Marshal(descriptor)
			w.Write(append([]byte(`{"domain_id":"bad",`), raw[1:]...))
			return
		case "bad-identity":
			d := descriptor
			d.NativeOrigin = strings.Repeat("f", 64)
			json.NewEncoder(w).Encode(d)
			return
		}
		json.NewEncoder(w).Encode(descriptor)
	}))
	domain := strings.Repeat("a", 64)
	descriptor = nativeidentity.Descriptor{DomainID: domain, PolicyDigest: strings.Repeat("b", 64), OwnerServerLeaf: nativeidentity.Fingerprint(server.TLS.Certificates[0].Certificate[0]), OwnerClientLeaf: nativeidentity.Fingerprint(owner.Certificates[0].Certificate[0]), NativeServerKey: strings.Repeat("c", 64), NativeOrigin: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain)), NativeScope: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain + "\x00default")), NativeServerName: "native-fixture", NativeServerVersion: "6.0.4"}
	cfg := nativeidentity.Configuration{Endpoint: server.URL, TLS: controller, OwnerServerLeaf: descriptor.OwnerServerLeaf, OwnerClientLeaf: descriptor.OwnerClientLeaf, PolicyDigest: descriptor.PolicyDigest, DomainID: domain}
	observation, e := nativeidentity.Observe(context.Background(), cfg)
	if e != nil || !observation.Valid() || observation.Record() != descriptor || observation.ObservedAt().IsZero() {
		t.Fatal("live observation", e)
	}
	if (nativeidentity.Observation{}).Valid() {
		t.Fatal("zero observation promoted")
	}
	for _, bad := range []string{"redirect", "duplicate", "bad-identity"} {
		mode = bad
		if _, e := nativeidentity.Observe(context.Background(), cfg); e == nil {
			t.Fatal("accepted", bad)
		}
	}
	mode = ""
	for _, bad := range []string{"pin", "policy", "domain", "insecure", "role"} {
		c := cfg
		c.TLS = cfg.TLS.Clone()
		switch bad {
		case "pin":
			c.OwnerServerLeaf = strings.Repeat("d", 64)
		case "policy":
			c.PolicyDigest = strings.Repeat("d", 64)
		case "domain":
			c.DomainID = strings.Repeat("d", 64)
		case "insecure":
			c.TLS.InsecureSkipVerify = true
		case "role":
			c.TLS = owner
		}
		if _, e := nativeidentity.Observe(context.Background(), c); e == nil {
			t.Fatal("accepted", bad)
		}
	}
}
