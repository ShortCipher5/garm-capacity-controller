package nativeretained

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
)

func observerCertificate(t *testing.T) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, IsCA: true, BasicConstraintsValid: true}
	raw, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{raw}, PrivateKey: key, Leaf: cert}, cert
}
func TestActualNonceBoundMTLSRetainedObservations(t *testing.T) {
	clientCert, clientCA := observerCertificate(t)
	clientRoots := x509.NewCertPool()
	clientRoots.AddCert(clientCA)
	var mode atomic.Value
	mode.Store("valid")
	owner := nativeidentity.Descriptor{DomainID: strings.Repeat("a", 64), PolicyDigest: strings.Repeat("b", 64), OwnerClientLeaf: strings.Repeat("c", 64), NativeServerKey: strings.Repeat("d", 64), NativeOrigin: "https://127.0.0.1:8443", NativeScope: "owned", NativeServerName: "native", NativeServerVersion: "6.0"}
	deleted := nativeidentity.RetirementOperationRecord{PermitID: "permit-one", IntentDigest: strings.Repeat("1", 64), ClaimID: strings.Repeat("2", 64), RequestDigest: strings.Repeat("3", 64), DomainID: owner.DomainID, PolicyDigest: owner.PolicyDigest, NativeName: "runner-one", ResourceID: "11111111-1111-4111-8111-111111111111", Dataset: "tank/domain/pool/containers/runner-one", DatasetGUID: "4", Step: "delete", OperationID: "22222222-2222-4222-8222-222222222222", Outcome: "succeeded"}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			t.Error("unverified request")
		}
		if r.Method != "GET" || r.URL.Path != PathPrefix+deleted.ClaimID+"/evidence" || len(r.URL.Query()) != 1 || !hex64(r.URL.Query().Get("nonce")) {
			t.Error("wrong concrete query", r.URL)
		}
		v := mode.Load().(string)
		if v == "unavailable" {
			http.Error(w, "closed", 503)
			return
		}
		if v == "redirect" {
			http.Redirect(w, r, "/other", 307)
			return
		}
		at := time.Now().UTC()
		d := Descriptor{Version: Version, Nonce: r.URL.Query().Get("nonce"), IssuedAt: at, Owner: owner, Delete: deleted, Qualification: "retained_observations_only", NamespaceInode: 123, NamespaceTerminated: true, NamespaceObservedAt: at, InstanceAbsent: true, InstanceObservedAt: at, StorageObservedAt: at, RootGUID: "1", RootQuotaBytes: 1024, RootUsedBytes: 64, RootAvailableBytes: 960, PoolGUID: "2", PoolLoadGUID: "3", PoolAllocatedBytes: 64, PoolFreeBytes: 960, PoolSizeBytes: 1024}

		switch v {
		case "wrong-limits":
			d.RootGUID = "01"
		case "unbounded-limits":
			d.RootQuotaBytes = 0
		case "foreign-capsule":
			d.Delete.ResourceID = "33333333-3333-4333-8333-333333333333"
		case "release":
			d.ReleaseAuthorized = true
		case "not-terminated":
			d.NamespaceTerminated = false
		case "missing-absence":
			d.InstanceAbsent = false
		case "old-storage":
			d.StorageObservedAt = at.Add(-6 * time.Second)
		case "wrong-operation":
			d.Delete.OperationID = "44444444-4444-4444-8444-444444444444"
		case "wrong-accounting":
			d.PoolFreeBytes = 1024
		case "replay":
			d.Nonce = strings.Repeat("0", 64)
		case "stale":
			d.IssuedAt = time.Now().Add(-6 * time.Second)
		case "future":
			d.IssuedAt = time.Now().Add(2 * time.Second)
		case "identity-only":
			d.Version = "native-domain/v1"
		case "owner":
			d.Owner.NativeScope = "another"
		case "missing-boundary":
			d.PoolLoadGUID = ""
		}
		raw, _ := json.Marshal(d)
		if v == "duplicate" {
			raw = []byte(strings.Replace(string(raw), `"nonce":`, `"nonce":"x","nonce":`, 1))
		}
		if v == "alias" {
			raw = []byte(strings.Replace(string(raw), `"owner":`, `"Owner":`, 1))
		}
		if v == "oversize" {
			raw = append(raw, []byte(strings.Repeat(" ", 17000))...)
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Write(raw)
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientRoots, MinVersion: tls.VersionTLS13}
	server.StartTLS()
	defer server.Close()
	owner.OwnerServerLeaf = hash(server.Certificate().Raw)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	config := Configuration{Endpoint: server.URL, TLS: &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{clientCert}}, Expected: owner, Delete: deleted}
	observation, err := Observe(context.Background(), config)
	if err != nil || !observation.Valid() || observation.Record().Owner != owner {
		t.Fatal("actual proof", err)
	}
	var empty Observation
	if empty.Valid() {
		t.Fatal("zero proof valid")
	}
	expired := observation
	expired.record.IssuedAt = time.Now().Add(-6 * time.Second)
	if expired.Valid() || expired.Record().Version != "" {
		t.Fatal("expired proof reused")
	}
	for _, v := range []string{"release", "not-terminated", "missing-absence", "old-storage", "wrong-operation", "wrong-accounting", "wrong-limits", "unbounded-limits", "foreign-capsule", "unavailable", "redirect", "replay", "stale", "future", "identity-only", "owner", "missing-boundary", "duplicate", "alias", "oversize"} {
		t.Run(v, func(t *testing.T) {
			mode.Store(v)
			if got, err := Observe(context.Background(), config); err == nil || got.Valid() {
				t.Fatal("untrusted descriptor accepted")
			}
		})
	}
	mode.Store("valid")
	for _, v := range []string{"wrong-pin", "missing-client", "wrong-client", "callback", "dynamic-cert", "execution-role"} {
		t.Run(v, func(t *testing.T) {
			c := config
			c.TLS = config.TLS.Clone()
			switch v {
			case "wrong-pin":
				c.Expected.OwnerServerLeaf = strings.Repeat("f", 64)
			case "missing-client":
				c.TLS.Certificates = nil
			case "wrong-client":
				wrong, _ := observerCertificate(t)
				c.TLS.Certificates = []tls.Certificate{wrong}
			case "callback":
				c.TLS.VerifyConnection = func(tls.ConnectionState) error { return nil }
			case "dynamic-cert":
				c.TLS.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &clientCert, nil }
			case "execution-role":
				c.Expected.OwnerClientLeaf = hash(clientCert.Certificate[0])
			}
			if got, err := Observe(context.Background(), c); err == nil || got.Valid() {
				t.Fatal("untrusted transport accepted")
			}
		})
	}
}

func TestRetainedDescriptorValidationCannotMintProof(t *testing.T) {
	if (Observation{}).Valid() {
		t.Fatal("zero")
	}
	o := Observation{verified: true, at: time.Now().Add(time.Second), record: Descriptor{IssuedAt: time.Now()}}
	if o.Valid() {
		t.Fatal("future monotonic age")
	}
}
