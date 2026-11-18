package adminview

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

func nativeEnrollmentHTTPFixture(t *testing.T) (*Handler, NativeEnrollmentRecipe, *atomic.Int64) {
	t.Helper()
	dir := t.TempDir()
	os.Chmod(dir, 0700)
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
	budget := admission.Resources{CPU: 4000, RAM: 8192, Disk: 20000, Runners: 4}
	config := admission.Config{Global: budget, Hosts: map[string]admission.Host{"host": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool": {Budget: budget, Hosts: []string{"host"}}}, MaxObservationAge: time.Minute}
	db, id := filepath.Join(dir, "db"), filepath.Join(dir, "identity")
	if e = admission.InitializeSQLite(db, id, config); e != nil {
		t.Fatal(e)
	}
	s, e := admission.OpenRuntimeSQLite(db, id)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	h, e := New(s, strings.Repeat("a", 64), nil, false)
	if e != nil {
		t.Fatal(e)
	}
	if e = h.EnableManagement(strings.Repeat("b", 64)); e != nil {
		t.Fatal(e)
	}
	recipe := NativeEnrollmentRecipe{ID: "first", Initial: true, Owners: []NativeEnrollmentOwner{{TrustIdentity: strings.Repeat("f", 64), Policy: policy, Transport: nativeidentity.Configuration{Endpoint: server.URL, TLS: controller, OwnerClientLeaf: d.OwnerClientLeaf, OwnerServerLeaf: d.OwnerServerLeaf, DomainID: d.DomainID, PolicyDigest: d.PolicyDigest}}}}
	return h, recipe, count
}
func TestNativeEnrollmentHTTPActualIdentityAndPartialSafeReplay(t *testing.T) {
	h, r, count := nativeEnrollmentHTTPFixture(t)
	existing := r
	existing.ID = "existing"
	existing.Initial = false
	if e := h.SetNativeEnrollmentRecipes([]NativeEnrollmentRecipe{r, existing}); e != nil {
		t.Fatal(e)
	}
	// Mutating caller credential bytes must not change frozen endpoint authority.
	r.Owners[0].Transport.TLS.Certificates[0].Certificate[0][0] ^= 0xff
	const path = "/admin/api/v1/native-enrollment"
	if w := request(t, h, "GET", path, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	read := strings.Repeat("a", 64)
	write := strings.Repeat("b", 64)
	w := request(t, h, "GET", path, read)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"readiness_authorized":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
	call := func(id string, seq int64) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]any{"recipe": id, "recipe_revision": h.nativeEnrollments[id].revision, "expected_sequence": seq})
		return post(t, h, "POST", path, write, string(b))
	}
	state, _ := h.store.ReadBrokerGrantState(context.Background())
	if w = call("existing", state.Sequence); w.Code != 409 || count.Load() != 0 {
		t.Fatal("unbound existing contacted", w.Body.String())
	}
	if w = call("first", state.Sequence+1); w.Code != 409 || count.Load() != 0 {
		t.Fatal("stale contacted", w.Body.String())
	}
	if w = call("first", state.Sequence); w.Code != 200 || !strings.Contains(w.Body.String(), `"confirmed_domains":1`) {
		t.Fatal(w.Code, w.Body.String())
	}
	domains, e := h.store.NativeDomainEnrollments(context.Background())
	if e != nil || len(domains) != 1 {
		t.Fatal(domains, e)
	}
	current, _ := h.store.ReadBrokerGrantState(context.Background())
	if current.Ready || current.Allocated.Global != (admission.Resources{}) {
		t.Fatal("activated")
	}
	if w = call("first", current.Sequence); w.Code != 409 {
		t.Fatal("initial replay accepted", w.Body.String())
	}
	if w = call("existing", current.Sequence); w.Code != 200 {
		t.Fatal("immutable retry failed", w.Body.String())
	}
	after, _ := h.store.ReadBrokerGrantState(context.Background())
	if after.Sequence != current.Sequence {
		t.Fatal("replay changed audit")
	}
	if w = post(t, h, "POST", path, read, `{}`); w.Code != 401 {
		t.Fatal("read token wrote")
	}
	if w = post(t, h, "POST", path, write, `{"recipe":"first","recipe":"existing"}`); w.Code != 400 {
		t.Fatal("duplicates allowed")
	}
	req := httptest.NewRequest("POST", "https://127.0.0.1"+path, strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://other.invalid")
	req.Header.Set("Authorization", "Bearer "+write)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatal("cross origin allowed", rec.Code)
	}
}

func TestNativeEnrollmentHTTPFailedObservationPreservesBarrier(t *testing.T) {
	h, r, _ := nativeEnrollmentHTTPFixture(t)
	r.Owners[0].Transport.OwnerServerLeaf = strings.Repeat("9", 64)
	if e := h.SetNativeEnrollmentRecipes([]NativeEnrollmentRecipe{r}); e != nil {
		t.Fatal(e)
	}
	before, _ := h.store.ReadBrokerGrantState(context.Background())
	body, _ := json.Marshal(map[string]any{"recipe": r.ID, "recipe_revision": h.nativeEnrollments[r.ID].revision, "expected_sequence": before.Sequence})
	w := post(t, h, "POST", "/admin/api/v1/native-enrollment", strings.Repeat("b", 64), string(body))
	if w.Code != 503 || !strings.Contains(w.Body.String(), `"partial":true`) || !strings.Contains(w.Body.String(), `"confirmed_domains":0`) {
		t.Fatal(w.Code, w.Body.String())
	}
	after, _ := h.store.ReadBrokerGrantState(context.Background())
	domains, seq, e := h.store.NativeDomainEnrollmentView(context.Background())
	if e != nil || len(domains) != 0 || seq != after.Sequence || after.Sequence <= before.Sequence || after.Ready || len(after.HostBindings) != 0 || after.Allocated.Global != (admission.Resources{}) {
		t.Fatal("failed observation changed authority", e)
	}
}

func TestNativeEnrollmentRecipeRefusesDynamicTrustAndUnreviewedInputs(t *testing.T) {
	h, r, _ := nativeEnrollmentHTTPFixture(t)
	r.Owners[0].Transport.TLS.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return nil, nil }
	if h.SetNativeEnrollmentRecipes([]NativeEnrollmentRecipe{r}) == nil {
		t.Fatal("dynamic TLS accepted")
	}
	r.Owners[0].Transport.TLS.GetClientCertificate = nil
	r.Owners[0].Transport.Endpoint += "?arbitrary=1"
	if h.SetNativeEnrollmentRecipes([]NativeEnrollmentRecipe{r}) == nil {
		t.Fatal("query URL accepted")
	}
}
