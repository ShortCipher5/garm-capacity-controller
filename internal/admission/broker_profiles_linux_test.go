package admission

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
)

const brokerProfileDowngradeSQL = nativeOperationDowngradeSQL + `
DROP TRIGGER broker_provider_not_owner;
DROP TABLE broker_pool_profiles;
DELETE FROM audit WHERE kind IN ('broker_profile_recorded','schema_migrated_v20');
`
const profilePool = "11111111-1111-4111-8111-111111111111"
const profileRepo = "22222222-2222-4222-8222-222222222222"

func poolProfileFixture(t *testing.T, images ...protectedboot.ImageDescriptor) (nativeFixture, BrokerPoolProfileRequest, stockidentity.Observation, map[int64]nativeapi.Policy) {
	t.Helper()
	spec := nativeapi.PolicySpec{Version: nativeapi.PolicyVersion, Controller: "controller", Host: "a", DomainID: strings.Repeat("a", 64),
		NativeSHA256: strings.Repeat("b", 64), LauncherSHA256: strings.Repeat("c", 64), ServerVersion: nativeapi.NativeVersion,
		Pools: map[string]nativeapi.Pool{profilePool: {Flavor: "small", Image: "prebuilt", Fingerprint: strings.Repeat("a", 64), Type: "container", Arch: "amd64", ExpectedProfiles: []string{"runner"}, StoragePool: "storage", Bridge: "bridge"}}}
	if len(images) > 0 {
		spec.Version = nativeapi.ProtectedPolicyVersion
		pool := spec.Pools[profilePool]
		pool.ProtectedImage = &images[0]
		spec.Pools[profilePool] = pool
	}
	policy, err := nativeapi.NewPolicy(spec)
	if err != nil {
		t.Fatal(err)
	}
	f := nativeExecutionFixture(t, policy.Digest())
	s := f.store
	state, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	config := state.Config
	config.Pools[profilePool] = Pool{Budget: Resources{CPU: 4000, RAM: 8192, Disk: 16384, Runners: 4}, Hosts: []string{"a"}}
	raw, _ := json.Marshal(config)
	if _, err = s.db.Exec("UPDATE metadata SET config=?", raw); err != nil {
		t.Fatal(err)
	}
	if err = s.ProfileUpsert(context.Background(), "small", 1000, 1024, 4096); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/pools/"+profilePool || r.Header.Get("Authorization") != "Bearer test" {
			t.Error("wrong request")
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": profilePool, "repo_id": profileRepo, "provider_name": "capacity", "flavor": "small", "image": "prebuilt", "os_type": "linux", "os_arch": "amd64", "max_runners": 10})
	}))
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	observation, err := stockidentity.Observe(context.Background(), stockidentity.Configuration{Endpoint: server.URL, Token: "test", Provider: "capacity", Pool: profilePool, TLS: &tls.Config{RootCAs: roots}})
	if err != nil {
		t.Fatal(err)
	}
	execution := placementRequest("unused").Execution
	execution.Name = ""
	request := BrokerPoolProfileRequest{SourceIdentity: observation.SourceIdentity(), SizeProfile: "small", Authority: StockProviderAuthority{Controller: "controller", PoolID: profilePool, ProviderName: "capacity", ClientFingerprint: strings.Repeat("f", 64)}, Execution: execution}
	return f, request, observation, map[int64]nativeapi.Policy{f.binding.Sequence: policy}
}
func TestBrokerProfileUsesObservedOwnershipAndSavedSizeAndImmutablePolicy(t *testing.T) {
	f, r, o, p := poolProfileFixture(t)
	ctx := context.Background()
	v, err := f.store.RegisterBrokerPoolProfile(ctx, r, o, p)
	if err != nil {
		t.Fatal(err)
	}
	if v.Repository != profileRepo || v.Pool != profilePool || v.Resources != (Resources{1000, 1024, 4096, 1}) || len(v.Domains) != 1 || v.Domains[0].Sequence != f.binding.Sequence {
		t.Fatalf("wrong mapping %+v", v)
	}
	replay, err := f.store.RegisterBrokerPoolProfile(ctx, r, o, p)
	if err != nil || replay.Sequence != v.Sequence {
		t.Fatal("exact retry changed revision", err)
	}
	state, err := f.store.ReadBrokerGrantState(ctx)
	if err != nil || state.PoolProfiles[profilePool].Sequence != v.Sequence {
		t.Fatal("not in coherent snapshot", err)
	}
	if err = f.store.ProfileUpsert(ctx, "small", 2000, 2048, 8192); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.RegisterBrokerPoolProfile(ctx, r, o, p); err == nil {
		t.Fatal("stale CAS replaced mapping")
	}
	r.ExpectedProfileSequence = v.Sequence
	next, err := f.store.RegisterBrokerPoolProfile(ctx, r, o, p)
	if err != nil || next.Previous != v.Sequence || next.Resources.CPU != 2000 {
		t.Fatal("versioned size update failed", err)
	}
	// A future domain enrollment cannot turn a configured provider leaf into
	// an owner credential, including before any provider origin was dispatched.
	ownerPayload, _ := json.Marshal(map[string]any{"spec": map[string]string{"owner_client_leaf": r.Authority.ClientFingerprint, "owner_server_leaf": strings.Repeat("8", 64)}})
	if _, e := f.store.db.Exec("INSERT INTO native_domain_bindings(domain_id,host,payload,payload_hash,record_sequence)VALUES(?,?,?,?,?)", strings.Repeat("9", 64), "other", string(ownerPayload), strings.Repeat("7", 64), 999999); e == nil || !strings.Contains(e.Error(), "broker provider leaf") {
		t.Fatal("future owner/provider role collision accepted", e)
	}
	// Historical mapping is permanent, not overwritten by the mutable size name.
	var count int
	if f.store.db.QueryRow("SELECT count(*) FROM broker_pool_profiles").Scan(&count) != nil || count != 2 {
		t.Fatal("history lost")
	}
	if _, err = f.store.db.Exec("DELETE FROM broker_pool_profiles"); err == nil {
		t.Fatal("history deletable")
	}
}
func TestBrokerProfileRefusesSelfAttestedOrDriftedInputs(t *testing.T) {
	for _, mode := range []string{"zero-observation", "source", "provider", "leaf-role", "image", "profiles", "domain", "empty-name"} {
		t.Run(mode, func(t *testing.T) {
			f, r, o, p := poolProfileFixture(t)
			switch mode {
			case "zero-observation":
				o = stockidentity.Observation{}
			case "source":
				r.SourceIdentity = strings.Repeat("a", 64)
			case "provider":
				r.Authority.ProviderName = "other"
			case "leaf-role":
				r.Authority.ClientFingerprint = f.binding.Spec.OwnerClientLeaf
			case "image":
				r.Execution.Image = "other"
			case "profiles":
				r.Execution.Profiles = []string{"default"}
			case "domain":
				p = map[int64]nativeapi.Policy{999: p[f.binding.Sequence]}
			case "empty-name":
				r.SizeProfile = "missing"
			}
			if _, err := f.store.RegisterBrokerPoolProfile(context.Background(), r, o, p); err == nil {
				t.Fatal("self-attested/drifted mapping accepted")
			}
			var count int
			if f.store.db.QueryRow("SELECT count(*) FROM broker_pool_profiles").Scan(&count) != nil || count != 0 {
				t.Fatal("failed registration partially persisted")
			}
		})
	}
}

func TestBrokerProfileBackupAndMissingHistoryFailClosed(t *testing.T) {
	f, r, o, p := poolProfileFixture(t)
	ctx := context.Background()
	v, err := f.store.RegisterBrokerPoolProfile(ctx, r, o, p)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sqliteBackupDir(t), "profiles.db")
	if err = f.store.BackupSQLite(ctx, path); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(sqliteBackupDir(t), "restored.db")
	if err = RestoreSQLite(ctx, path, restored); err != nil {
		t.Fatal(err)
	}
	copy, err := OpenSQLite(restored, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	state, err := copy.ReadBrokerGrantState(ctx)
	if err != nil || state.PoolProfiles[profilePool].Sequence != v.Sequence || state.Ready {
		t.Fatal("restored mapping lost or activated", err)
	}
	if _, err = f.store.db.Exec("DROP TRIGGER broker_profile_no_delete; DELETE FROM broker_pool_profiles"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.CurrentBrokerPoolProfiles(ctx); err == nil {
		t.Fatal("deleted history silently forgotten")
	}
}

func TestBrokerProfileMigrationFromRealSchema19ClosesReadiness(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	if _, err := s.db.Exec(brokerProfileDowngradeSQL + "PRAGMA user_version=19;"); err != nil {
		t.Fatal(err)
	}
	path := s.path
	s.Close()
	reopened, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, err := reopened.ReadBrokerGrantState(ctx)
	if err != nil || state.Ready || len(state.PoolProfiles) != 0 {
		t.Fatal("migration activated or fabricated mapping", err)
	}
	var version int
	if reopened.db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != sqliteCurrentVersion {
		t.Fatal("migration version")
	}
}

func TestBrokerProfileProtectedTemplateWithoutFabricatedJobPayload(t *testing.T) {
	files := map[string]string{"bin/Runner.Listener": strings.Repeat("1", 64), "bin/Runner.Listener.dll": strings.Repeat("3", 64)}
	raw, _ := json.Marshal(files)
	image := protectedboot.ImageDescriptor{InitSHA256: strings.Repeat("2", 64), LauncherSHA256: strings.Repeat("2", 64), BundleDigest: protectedboot.Hash(raw), RunnerRelease: "2.321.0", UID: 1001, GID: 1001, Files: files}
	f, r, o, p := poolProfileFixture(t, image)
	v, err := f.store.RegisterBrokerPoolProfile(context.Background(), r, o, p)
	if err != nil || v.Sequence == 0 || v.Domains[0].PolicyDigest != p[f.binding.Sequence].Digest() {
		t.Fatal("protected template registration refused", err)
	}
	r.ExpectedProfileSequence = v.Sequence
	r.Execution.Image = "unreviewed"
	if _, err = f.store.RegisterBrokerPoolProfile(context.Background(), r, o, p); err == nil {
		t.Fatal("protected template mismatch accepted")
	}
}
