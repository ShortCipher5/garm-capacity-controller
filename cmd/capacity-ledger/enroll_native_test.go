package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func nativeEnrollmentFixture(t *testing.T) ([]string, *admission.SQLiteStore, *nativeEnrollmentConfig, *nativeidentity.Descriptor, *atomic.Int64) {
	t.Helper()
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	write := func(n string, b []byte) string {
		p := filepath.Join(dir, n)
		if e := os.WriteFile(p, b, 0600); e != nil {
			t.Fatal(e)
		}
		return p
	}
	files := map[string]string{"bin/Runner.Listener": strings.Repeat("1", 64), "bin/Runner.Listener.dll": strings.Repeat("2", 64)}
	raw, _ := json.Marshal(files)
	image := protectedboot.ImageDescriptor{InitSHA256: strings.Repeat("3", 64), LauncherSHA256: strings.Repeat("4", 64), BundleDigest: protectedboot.Hash(raw), RunnerRelease: "2.321.0", UID: 1001, GID: 1001, Files: files}
	spec := nativeapi.PolicySpec{Version: nativeapi.ProtectedPolicyVersion, Controller: "controller", Host: "host", DomainID: strings.Repeat("a", 64), NativeSHA256: strings.Repeat("b", 64), LauncherSHA256: strings.Repeat("c", 64), ServerVersion: nativeapi.NativeVersion, Pools: map[string]nativeapi.Pool{"pool": {Flavor: "small", Image: "runner", Fingerprint: strings.Repeat("d", 64), Type: "container", Arch: "amd64", ExpectedProfiles: []string{"runner"}, StoragePool: "storage", Bridge: "bridge", ZFSDataset: "pool/root", ProtectedImage: &image}}}
	policy, e := nativeapi.NewPolicy(spec)
	if e != nil {
		t.Fatal(e)
	}
	var d nativeidentity.Descriptor
	count := &atomic.Int64{}
	server, controller, owner := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.URL.Path != nativeidentity.Path {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(d)
	}))
	d = nativeidentity.Descriptor{DomainID: spec.DomainID, PolicyDigest: policy.Digest(), OwnerClientLeaf: nativeidentity.Fingerprint(owner.Certificates[0].Certificate[0]), OwnerServerLeaf: nativeidentity.Fingerprint(server.TLS.Certificates[0].Certificate[0]), NativeServerKey: strings.Repeat("5", 64), NativeOrigin: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + spec.DomainID)), NativeScope: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + spec.DomainID + "\x00default")), NativeServerName: "fixture-native", NativeServerVersion: nativeapi.NativeVersion}
	key, e := x509.MarshalPKCS8PrivateKey(controller.Certificates[0].PrivateKey)
	if e != nil {
		t.Fatal(e)
	}
	cfg := &nativeEnrollmentConfig{Owners: []nativeEnrollmentOwner{{Endpoint: server.URL, OwnerClientLeaf: d.OwnerClientLeaf, OwnerServerLeaf: d.OwnerServerLeaf, Policy: spec, TLS: securefiles.TLSFiles{CA: write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: controller.Certificates[0].Certificate[1]})), Certificate: write("client.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: controller.Certificates[0].Certificate[0]})), Key: write("client.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))}}}}
	b, _ := json.Marshal(cfg)
	config := write("owners.json", b)
	database, identity := filepath.Join(dir, "ledger.db"), filepath.Join(dir, "identity")
	budget := admission.Resources{CPU: 4000, RAM: 8192, Disk: 20000, Runners: 4}
	initial := admission.Config{Global: budget, Hosts: map[string]admission.Host{"host": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool": {Budget: budget, Hosts: []string{"host"}}}, MaxObservationAge: time.Minute}
	if e = admission.InitializeSQLite(database, identity, initial); e != nil {
		t.Fatal(e)
	}
	store, e := admission.OpenRuntimeSQLite(database, identity)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	return []string{"enroll-native", "--source", database, "--identity-file", identity, "--config", config}, store, cfg, &d, count
}
func TestNativeEnrollmentCLIActualPinnedFreshHostAndReplay(t *testing.T) {
	args, s, _, descriptor, count := nativeEnrollmentFixture(t)
	ctx := context.Background()
	var out, diagnostic bytes.Buffer
	if run(ctx, args, &out, &diagnostic) != 1 || count.Load() != 0 {
		t.Fatal("unbound host accepted or contacted", diagnostic.String())
	}
	initial := append(append([]string{}, args...), "--bind-initial-hosts")
	out.Reset()
	diagnostic.Reset()
	if run(ctx, initial, &out, &diagnostic) != 0 || count.Load() != 1 {
		t.Fatal("fresh enrollment failed", diagnostic.String())
	}
	snap, e := s.Snapshot(ctx)
	if e != nil || snap.Ready || snap.Allocated.Global != (admission.Resources{}) {
		t.Fatal("enrollment activated or charged", e)
	}
	binding, e := s.LookupHostBinding(ctx, "host")
	if e != nil || binding.ServerKeyFingerprint != descriptor.NativeServerKey {
		t.Fatal("actual native binding missing", e)
	}
	beforeState, e := s.ReadBrokerGrantState(ctx)
	if e != nil {
		t.Fatal(e)
	}
	before := beforeState.Sequence
	out.Reset()
	diagnostic.Reset()
	if run(ctx, args, &out, &diagnostic) != 0 || count.Load() != 2 {
		t.Fatal("exact existing enrollment retry failed", diagnostic.String())
	}
	after, e := s.ReadBrokerGrantState(ctx)
	if e != nil || after.Sequence != before || after.Ready {
		t.Fatal("retry mutated enrollment/readiness", e)
	}
	descriptor.NativeServerKey = strings.Repeat("8", 64)
	out.Reset()
	diagnostic.Reset()
	if run(ctx, args, &out, &diagnostic) != 1 {
		t.Fatal("different native backend rebound")
	}
	old, e := s.LookupHostBinding(ctx, "host")
	if e != nil || old != binding {
		t.Fatal("rebind changed record", e)
	}
	out.Reset()
	diagnostic.Reset()
	if run(ctx, initial, &out, &diagnostic) != 1 {
		t.Fatal("initial mode adopted existing bindings")
	}
}
func TestNativeEnrollmentCLIRejectsAmbiguousConfigWithoutContact(t *testing.T) {
	args, s, _, _, count := nativeEnrollmentFixture(t)
	for _, raw := range []string{`{"owners":[],"owners":[]}`, `{"Owners":[]}`, `{"owners":[],"activate":true}`} {
		if e := os.WriteFile(args[len(args)-1], []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
		var out, diagnostic bytes.Buffer
		if run(context.Background(), append(append([]string{}, args...), "--bind-initial-hosts"), &out, &diagnostic) != 1 || count.Load() != 0 {
			t.Fatal("invalid config accepted/contacted")
		}
	}
	snap, e := s.Snapshot(context.Background())
	if e != nil || snap.Ready || len(snap.HostBindings) != 0 {
		t.Fatal("invalid config changed identity")
	}
}
