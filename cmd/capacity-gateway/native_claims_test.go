package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecontrol"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecontroller"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

func claimTestPolicy() admission.Config {
	r := admission.Resources{CPU: 1000, RAM: 1024, Disk: 4096, Runners: 1}
	return admission.Config{MaxObservationAge: time.Hour, Global: r, Hosts: map[string]admission.Host{"host": {Budget: r, Mode: "enabled", Healthy: true, ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool": {Budget: r, Hosts: []string{"host"}}}}
}
func TestNativeClaimRuntimeRefusesBeforeKeysOrListener(t *testing.T) {
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	db, id := filepath.Join(dir, "ledger"), filepath.Join(dir, "identity")
	if e := admission.InitializeSQLite(db, id, claimTestPolicy()); e != nil {
		t.Fatal(e)
	}
	store, e := admission.OpenRuntimeSQLite(db, id)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	c := configuration{NativeClaims: &nativeClaimConfiguration{Listen: "127.0.0.1:0", TLS: securefiles.TLSFiles{CA: "/missing", Certificate: "/missing", Key: "/missing"}, DomainBindingSequences: []int64{1}}}
	before, e := store.Snapshot(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	if service, e := buildNativeClaims(t.Context(), c, store); service != nil || !errors.Is(e, nativecontroller.ErrUnqualified) {
		t.Fatal("runtime guard not first", e)
	}
	if e := serve(t.Context(), c, false, false); e == nil {
		t.Fatal("production opened native listener")
	}
	after, e := store.Snapshot(t.Context())
	if e != nil || len(after.Audit) != len(before.Audit) {
		t.Fatal("refusal changed ledger", e)
	}
}
func TestNativeClaimConfigurationCannotMixListenersOrRepeatDomains(t *testing.T) {
	for _, c := range []nativeClaimConfiguration{
		{Listen: "0.0.0.0:9443", DomainBindingSequences: []int64{1}},
		{Listen: "127.0.0.1:9443", DomainBindingSequences: []int64{1, 1}},
		{Listen: "127.0.0.1:9443", DomainBindingSequences: []int64{0}},
		{Listen: "127.0.0.1:9443"},
		{Listen: "127.0.0.1:9000", DomainBindingSequences: []int64{1}},
		{Listen: "127.0.0.1:9001", DomainBindingSequences: []int64{1}},
	} {
		if validateNativeClaimConfiguration(&c, "127.0.0.1:9000", "127.0.0.1:9001") == nil {
			t.Fatal("invalid owner listener", c)
		}
	}
	if e := validateNativeClaimConfiguration(&nativeClaimConfiguration{Listen: "127.0.0.1:9443", DomainBindingSequences: []int64{1, 2}}, "127.0.0.1:9000", "127.0.0.1:9001"); e != nil {
		t.Fatal(e)
	}
}
func TestNativeClaimListenerUsesVerifiedOwnerOnlyTLSAndJoins(t *testing.T) {
	store, e := admission.OpenSQLite(filepath.Join(t.TempDir(), "ledger"), claimTestPolicy())
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	fixture, controller, owner := testsupport.NativeTLS(t, http.NotFoundHandler())
	allowed := map[string]bool{nativeidentity.Fingerprint(owner.Certificates[0].Certificate[0]): true}
	server, e := newNativeClaimServer(store, fixture.TLS, allowed)
	if e != nil {
		t.Fatal(e)
	}
	// Mutating caller scope after construction cannot expand the handler.
	allowed[nativeidentity.Fingerprint(controller.Certificates[0].Certificate[0])] = true
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveNativeClaimListener(ctx, server, listener) }()
	endpoint := "https://" + listener.Addr().String()
	request := func(trust *tls.Config, path string) int {
		tr := &http.Transport{TLSClientConfig: trust, DisableKeepAlives: true}
		defer tr.CloseIdleConnections()
		client := &http.Client{Transport: tr, Timeout: time.Second}
		req, _ := http.NewRequest("POST", endpoint+path, strings.NewReader(`{"permit_id":"`+strings.Repeat("a", 32)+`","envelope_digest":"`+strings.Repeat("b", 64)+`"}`))
		req.Header.Set("Content-Type", "application/json")
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		io.Copy(io.Discard, res.Body)
		return res.StatusCode
	}
	if code := request(controller, nativecontrol.ClaimPath); code != 401 {
		t.Fatal("controller/provider role crossed owner scope", code)
	}
	// Correct owner reaches canonical ledger but no fabricated permit can succeed.
	if code := request(owner, nativecontrol.ClaimPath); code != 409 {
		t.Fatal("canonical claim not consulted", code)
	}
	if code := request(owner, "/v1/provider/create"); code != 404 {
		t.Fatal("provider route exposed", code)
	}
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("listener did not join")
	}
	if conn, e := net.DialTimeout("tcp", listener.Addr().String(), time.Second); e == nil {
		conn.Close()
		t.Fatal("listener remained open")
	}
	state, e := store.Snapshot(t.Context())
	if e != nil || len(state.Reservations) != 0 {
		t.Fatal("unbound request allocated", e)
	}
}

func writeClaimTLS(t *testing.T, pair tls.Certificate, ca []byte) securefiles.TLSFiles {
	t.Helper()
	dir := t.TempDir()
	key, e := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	if e != nil {
		t.Fatal(e)
	}
	files := securefiles.TLSFiles{CA: filepath.Join(dir, "ca"), Certificate: filepath.Join(dir, "cert"), Key: filepath.Join(dir, "key")}
	for path, data := range map[string][]byte{files.CA: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca}), files.Certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]}), files.Key: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})} {
		if e := os.WriteFile(path, data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	return files
}
func TestNativeClaimBuilderUsesActualImmutableEnrollmentAndSeparateRoles(t *testing.T) {
	store, e := admission.OpenSQLite(filepath.Join(t.TempDir(), "ledger"), claimTestPolicy())
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	var descriptor nativeidentity.Descriptor
	discovery, controller, owner := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(descriptor) }))
	domain, policy := strings.Repeat("a", 64), strings.Repeat("b", 64)
	descriptor = nativeidentity.Descriptor{DomainID: domain, PolicyDigest: policy, OwnerClientLeaf: nativeidentity.Fingerprint(owner.Certificates[0].Certificate[0]), OwnerServerLeaf: nativeidentity.Fingerprint(discovery.TLS.Certificates[0].Certificate[0]), NativeServerKey: strings.Repeat("c", 64), NativeOrigin: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain)), NativeScope: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain + "\x00default")), NativeServerName: "owner", NativeServerVersion: "6.0.4"}
	fence, e := store.BeginReconciliation(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	obs, e := nativeidentity.Observe(t.Context(), nativeidentity.Configuration{Endpoint: discovery.URL, TLS: controller, DomainID: domain, PolicyDigest: policy, OwnerClientLeaf: descriptor.OwnerClientLeaf, OwnerServerLeaf: descriptor.OwnerServerLeaf})
	if e != nil {
		t.Fatal(e)
	}
	measured, e := admission.ObservedNativeHostBinding("host", obs)
	if e != nil {
		t.Fatal(e)
	}
	if e = store.BindHostIdentities(t.Context(), fence, []admission.HostBinding{measured}); e != nil {
		t.Fatal(e)
	}
	host, e := store.LookupHostBinding(t.Context(), "host")
	if e != nil {
		t.Fatal(e)
	}
	binding, e := store.EnrollNativeDomain(t.Context(), admission.NativeDomainSpec{Controller: "controller", Host: "host", HostBindingSequence: host.Sequence, DomainID: domain, PolicyDigest: policy, OwnerClientLeaf: descriptor.OwnerClientLeaf, OwnerServerLeaf: descriptor.OwnerServerLeaf}, obs)
	if e != nil {
		t.Fatal(e)
	}
	claimTLS, _, _ := testsupport.NativeTLS(t, http.NotFoundHandler())
	providerTLS, _, _ := testsupport.NativeTLS(t, http.NotFoundHandler())
	c := configuration{Controller: "controller", Listen: "127.0.0.1:9000", ServerTLS: writeClaimTLS(t, providerTLS.TLS.Certificates[0], providerTLS.TLS.Certificates[0].Certificate[1]), NativeClaims: &nativeClaimConfiguration{Listen: "127.0.0.1:9443", TLS: writeClaimTLS(t, claimTLS.TLS.Certificates[0], discovery.TLS.Certificates[0].Certificate[1]), DomainBindingSequences: []int64{binding.Sequence}}}
	if service, e := buildNativeClaims(t.Context(), c, store); e != nil || service == nil {
		t.Fatal("actual enrollment rejected", e)
	}
	c.ProviderFingerprints = []string{descriptor.OwnerClientLeaf}
	if _, e := buildNativeClaims(t.Context(), c, store); e == nil {
		t.Fatal("provider/owner leaf overlap")
	}
	c.ProviderFingerprints = nil
	for _, leaf := range []string{descriptor.OwnerClientLeaf, descriptor.OwnerServerLeaf, nativeidentity.Fingerprint(claimTLS.TLS.Certificates[0].Certificate[0])} {
		c.ProviderInspectionFingerprint = leaf
		if _, e := buildNativeClaims(t.Context(), c, store); e == nil {
			t.Fatal("inspection role overlaps native claim authority")
		}
	}
	c.ProviderInspectionFingerprint = ""
	c.NativeStock = map[string]nativeStockPoolConfiguration{"pool": {Hosts: map[string]nativeStockHostConfiguration{"host": {OwnerTLS: writeClaimTLS(t, owner.Certificates[0], discovery.TLS.Certificates[0].Certificate[1])}}}}
	if _, e := buildNativeClaims(t.Context(), c, store); e == nil {
		t.Fatal("outbound controller/owner leaf overlap")
	}
	c.NativeStock = nil
	c.Hosts = map[string]hostConfiguration{"host": {TLS: writeClaimTLS(t, owner.Certificates[0], discovery.TLS.Certificates[0].Certificate[1])}}
	if _, e := buildNativeClaims(t.Context(), c, store); e == nil {
		t.Fatal("native host client/owner leaf overlap")
	}
	c.Hosts = nil
	c.Controller = "wrong"
	if _, e := buildNativeClaims(t.Context(), c, store); e == nil {
		t.Fatal("cross-controller enrollment")
	}
	c.Controller = "controller"
	c.NativeClaims.DomainBindingSequences = []int64{binding.Sequence + 100}
	if _, e := buildNativeClaims(t.Context(), c, store); e == nil {
		t.Fatal("caller invented domain")
	}
}
