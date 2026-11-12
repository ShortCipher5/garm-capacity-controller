package admission

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

const evidenceInstanceUUID = "11111111-1111-4111-8111-111111111111"
const evidenceGeneration = "22222222-2222-4222-8222-222222222222"
const evidenceOtherGeneration = "33333333-3333-4333-8333-333333333333"

// These are actual disposable credential-verified HTTPS requests, not an opaque
// evidence constructor or an operator supplied 'verified' boolean.
func evidenceTLS(t *testing.T, handler http.Handler) (*httptest.Server, *tls.Config) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "disposable-evidence-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	raw, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(authority)
	certificate := func(serial int64, usage x509.ExtKeyUsage) tls.Certificate {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "disposable-evidence"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		encoded, err := x509.CreateCertificate(rand.Reader, leaf, authority, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{encoded, raw}, PrivateKey: key}
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate(2, x509.ExtKeyUsageServerAuth)}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{certificate(3, x509.ExtKeyUsageClientAuth)}, MinVersion: tls.VersionTLS12}
}

type createEvidenceFixture struct {
	store      *SQLiteStore
	request    Request
	client     *incusapi.Client
	unpinned   *incusapi.Client
	serverName atomic.Value
	kind       atomic.Value
	binding    HostBinding
	permit     DispatchPermit
	writes     atomic.Int32
	generation atomic.Value
}

func newCreateEvidenceFixture(t *testing.T, state string, bound bool) *createEvidenceFixture {
	t.Helper()
	f := &createEvidenceFixture{request: request(evidenceDigest([]byte("controller-1\x00app\x00runner-1")))}
	f.generation.Store(evidenceGeneration)
	f.serverName.Store("evidence-daemon")
	f.kind.Store("Creating instance")
	config := fixture(time.Now())
	delete(config.Hosts, "b")
	for key, pool := range config.Pools {
		pool.Hosts = []string{"a"}
		config.Pools[key] = pool
	}
	f.store = sqliteFixtureStore(t, config, false)
	if err := os.Chmod(filepath.Dir(f.store.path), 0700); err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	var credentials *tls.Config
	write := func(w http.ResponseWriter, metadata any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "sync", "status_code": 200, "metadata": metadata})
	}
	server, credentials = evidenceTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			t.Error("client authentication absent")
		}
		if r.URL.Query().Get("project") != "runners" {
			t.Error("wrong project")
		}
		if r.Method != http.MethodGet {
			f.writes.Add(1)
			if r.Method == http.MethodPost && r.URL.Path == "/1.0/instances" {
				_ = json.NewEncoder(w).Encode(map[string]any{"type": "async", "status_code": 100, "operation": "/1.0/operations/" + testOperationID + "?project=runners", "metadata": map[string]any{"id": testOperationID}})
				return
			}
			t.Error("unexpected write")
			http.Error(w, "forbidden", 500)
			return
		}
		switch r.URL.Path {
		case "/1.0":
			leaf := server.Certificate()
			digest := sha256.Sum256(leaf.Raw)
			write(w, map[string]any{"api_version": "1.0", "auth": "trusted", "api_extensions": []string{"instance_create_start"}, "environment": map[string]any{"server_clustered": false, "server_name": f.serverName.Load(), "server_version": "6.0.4", "certificate": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})), "certificate_fingerprint": fmt.Sprintf("%x", digest)}})
		case "/1.0/instances/runner-1":
			write(w, map[string]any{"name": "runner-1", "status_code": 103, "created_at": "2026-01-01T00:00:00Z", "expanded_config": map[string]any{"limits.cpu": "1", "limits.memory": "2048MiB", "user.runner-controller-id": "controller-1", "user.runner-pool-id": f.request.Pool, "user.garm-capacity.reservation": f.request.Key, "volatile.uuid": evidenceInstanceUUID, "volatile.uuid.generation": f.generation.Load(), "user.user-data": "bootstrap-secret-not-journaled"}})
		case "/1.0/operations/" + testOperationID:
			write(w, map[string]any{"id": testOperationID, "class": "task", "description": f.kind.Load(), "status_code": 200, "status": "Success", "err": "", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "resources": map[string][]string{"instances": {"/1.0/instances/runner-1?project=runners"}}, "metadata": map[string]any{"private": "operation-secret-not-journaled"}})
		default:
			http.NotFound(w, r)
		}
	}))
	var err error
	f.client, err = incusapi.New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.client.Close)
	f.unpinned, err = incusapi.New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.unpinned.Close)
	physical, err := f.client.PhysicalIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fence, err := f.store.BeginReconciliation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	binding := HostBinding{Host: "a", ServerKeyFingerprint: physical.ServerKeyFingerprint(), OriginFingerprint: physical.OriginFingerprint(), ScopeFingerprint: physical.ScopeFingerprint(), ServerName: physical.ServerName(), ServerVersion: physical.ServerVersion(), ObservedAt: time.Now()}
	if err = f.store.BindHostIdentities(context.Background(), fence, []HostBinding{binding}); err != nil {
		t.Fatal(err)
	}
	f.binding, err = f.store.LookupHostBinding(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.client.PinPhysicalIdentity(physical); err != nil {
		t.Fatal(err)
	}
	if err = f.store.CompleteReconciliation(context.Background(), fence, InventorySnapshot{Hosts: observations(config, time.Now())}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.Admit(context.Background(), f.request); err != nil {
		t.Fatal(err)
	}
	var permit *DispatchPermit
	if bound {
		permit, err = f.store.ClaimDispatchBound(context.Background(), f.request, f.binding)
	} else {
		permit, err = f.store.ClaimDispatch(context.Background(), f.request)
	}
	if err != nil {
		t.Fatal(err)
	}
	f.permit = *permit
	created, err := f.client.Create(context.Background(), incusapi.InstanceSpec{Name: "runner-1", Type: "container", Profiles: []string{"runner-base"}, ImageFingerprint: strings.Repeat("a", 64), CPUMillis: 1000, RAMMiB: 2048, UserData: "#cloud-config\nbootstrap-secret-not-journaled", ControllerID: "controller-1", PoolID: f.request.Pool, ReservationKey: f.request.Key})
	if err != nil {
		t.Fatal(err)
	}
	if state != "intent" {
		if err = f.store.RecordDispatchOutcome(context.Background(), *permit, DispatchReceipt{State: state, OperationID: created.OperationID, ResourceID: created.ResourceID}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}
func (f *createEvidenceFixture) observe(t *testing.T) incusapi.TerminalOperationObservation {
	t.Helper()
	return f.observeWith(t, f.client, incusapi.RecoveryCreate)
}
func (f *createEvidenceFixture) observeWith(t *testing.T, c *incusapi.Client, kind incusapi.RecoveryOperationKind) incusapi.TerminalOperationObservation {
	t.Helper()
	subject, err := c.ObserveInstanceIdentity(context.Background(), incusapi.IdentityExpectation{Name: "runner-1", ControllerID: "controller-1", PoolID: f.request.Pool, ReservationKey: f.request.Key, CPUMillis: 1000, RAMMiB: 2048})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := c.CaptureTerminalOperation(context.Background(), subject, testOperationID, kind)
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func TestCreateEvidenceActualTLSAppendNoAccountingOrRedispatch(t *testing.T) {
	for _, state := range []string{"submitted", "confirmed", "unknown"} {
		t.Run(state, func(t *testing.T) {
			f := newCreateEvidenceFixture(t, state, true)
			proof := f.observe(t)
			before := sqliteSnapshot(t, f.store)
			dispatchBefore, err := f.store.LookupDispatch(context.Background(), f.request.Key)
			if err != nil {
				t.Fatal(err)
			}
			entry, err := f.store.StoreCreateObservation(context.Background(), f.request, proof)
			if err != nil || entry.Replay {
				t.Fatalf("append: %+v %v", entry, err)
			}
			if entry.Observation.Identity.Subject.UUID != evidenceInstanceUUID || entry.Observation.OperationID != testOperationID {
				t.Fatal("whitelist identity lost")
			}
			again, err := f.store.StoreCreateObservation(context.Background(), f.request, f.observe(t))
			if err != nil || !again.Replay || !again.StoredAt.Equal(entry.StoredAt) || !reflect.DeepEqual(again.Observation, entry.Observation) {
				t.Fatalf("immutable replay: %+v %v", again, err)
			}
			after := sqliteSnapshot(t, f.store)
			dispatchAfter, _ := f.store.LookupDispatch(context.Background(), f.request.Key)
			if !reflect.DeepEqual(before.Reservations, after.Reservations) || !reflect.DeepEqual(before.Inventory, after.Inventory) || !reflect.DeepEqual(before.Allocated, after.Allocated) || before.Ready != after.Ready || before.Epoch != after.Epoch || dispatchBefore != dispatchAfter || f.writes.Load() != 1 {
				t.Fatal("observation mutated accounting or replayed a backend write")
			}
			got, err := f.store.LookupCreateEvidence(context.Background(), f.request.Key)
			if err != nil || !reflect.DeepEqual(got, entry) {
				t.Fatal("indexed readback mismatch", err)
			}
			other, err := OpenSQLite(f.store.path, Config{})
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			got, err = other.LookupCreateEvidence(context.Background(), f.request.Key)
			if err != nil || !reflect.DeepEqual(got, entry) {
				t.Fatal("restart lost evidence", err)
			}
			var count int
			if err := f.store.db.QueryRow("SELECT count(*) FROM audit WHERE kind='create_observation_recorded'").Scan(&count); err != nil || count != 1 {
				t.Fatal("replay duplicate audit", count, err)
			}
			var data []byte
			if err := f.store.db.QueryRow("SELECT record FROM create_evidence").Scan(&data); err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"bootstrap-secret", "operation-secret", "PRIVATE KEY", "https://"} {
				if strings.Contains(string(data), secret) {
					t.Fatal("journal retained raw/secret data")
				}
			}
			for _, statement := range []string{"UPDATE create_evidence SET digest='changed'", "DELETE FROM create_evidence"} {
				if _, err := f.store.db.Exec(statement); err == nil {
					t.Fatal("evidence mutable")
				}
			}
		})
	}
}

func TestCreateEvidenceRejectsUnknownUnboundAndConflictingHistory(t *testing.T) {
	for _, mode := range []string{"zero", "delete", "same-key-name-drift", "json-forged", "no-known-operation", "intent", "unbound", "wrong-request", "wrong-operation", "wrong-resource", "generation-replacement", "binding-sequence", "binding-scope", "binding-name", "binding-version", "future-capture", "audit-rollback"} {
		t.Run(mode, func(t *testing.T) {
			state := "unknown"
			if mode == "intent" {
				state = "intent"
			}
			f := newCreateEvidenceFixture(t, state, mode != "unbound")
			proof := f.observe(t)
			req := f.request
			switch mode {
			case "zero":
				proof = incusapi.TerminalOperationObservation{}
			case "delete":
				f.kind.Store("Deleting instance")
				proof = f.observeWith(t, f.client, incusapi.RecoveryDelete)
			case "same-key-name-drift":
				f.serverName.Store("replacement-daemon")
				if _, err := f.client.PhysicalIdentity(context.Background()); err == nil {
					t.Fatal("pinned client accepted same-key name drift")
				}
				proof = f.observeWith(t, f.unpinned, incusapi.RecoveryCreate)
				if !proof.Valid() {
					t.Fatal("fixture failed to produce authenticated but differently bound observation")
				}

			case "json-forged":
				raw, _ := json.Marshal(proof.Record())
				proof = incusapi.TerminalOperationObservation{}
				if err := json.Unmarshal(raw, &proof); err != nil {
					t.Fatal(err)
				}
			case "no-known-operation":
				if _, err := f.store.db.Exec("UPDATE dispatches SET operation_id='' WHERE reservation_key=?", req.Key); err != nil {
					t.Fatal(err)
				}
			case "wrong-request":
				req.Resources.CPU++
			case "wrong-operation":
				if _, err := f.store.db.Exec("UPDATE dispatches SET operation_id=? WHERE reservation_key=?", evidenceOtherGeneration, req.Key); err != nil {
					t.Fatal(err)
				}
			case "wrong-resource":
				if _, err := f.store.db.Exec("UPDATE dispatches SET resource_id='other' WHERE reservation_key=?", req.Key); err != nil {
					t.Fatal(err)
				}
			case "generation-replacement":
				if _, err := f.store.StoreCreateObservation(context.Background(), req, proof); err != nil {
					t.Fatal(err)
				}
				f.generation.Store(evidenceOtherGeneration)
				proof = f.observe(t)
			case "binding-sequence":
				if _, err := f.store.db.Exec("UPDATE dispatches SET host_binding_sequence=NULL WHERE reservation_key=?", req.Key); err == nil {
					t.Fatal("dispatch binding provenance mutable")
				}
				if _, err := f.store.db.Exec("DROP TRIGGER dispatch_host_binding_is_immutable"); err != nil {
					t.Fatal(err)
				}
				if _, err := f.store.db.Exec("UPDATE dispatches SET host_binding_sequence=NULL WHERE reservation_key=?", req.Key); err != nil {
					t.Fatal(err)
				}
			case "binding-scope", "binding-name", "binding-version":
				if _, err := f.store.db.Exec("DROP TRIGGER host_bindings_immutable_update"); err != nil {
					t.Fatal(err)
				}
				statement := "UPDATE host_bindings SET scope='" + strings.Repeat("c", 64) + "'"
				if mode == "binding-name" {
					statement = "UPDATE host_bindings SET server_name='changed'"
				}
				if mode == "binding-version" {
					statement = "UPDATE host_bindings SET server_version='6.0.5'"
				}
				if _, err := f.store.db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			case "future-capture":
				f.store.now = func() time.Time { return time.Now().Add(-time.Minute) }
			case "audit-rollback":
				if _, err := f.store.db.Exec("CREATE TRIGGER reject_evidence_audit BEFORE INSERT ON audit WHEN NEW.kind='create_observation_recorded' BEGIN SELECT RAISE(ABORT,'test rollback'); END"); err != nil {
					t.Fatal(err)
				}
			}
			before := sqliteSnapshot(t, f.store)
			if _, err := f.store.StoreCreateObservation(context.Background(), req, proof); err == nil {
				t.Fatal("unqualified evidence accepted")
			}
			after := sqliteSnapshot(t, f.store)
			if !reflect.DeepEqual(before.Allocated, after.Allocated) || !reflect.DeepEqual(before.Reservations, after.Reservations) || f.writes.Load() != 1 {
				t.Fatal("denied observation changed charge or redispatched")
			}
			var count int
			if err := f.store.db.QueryRow("SELECT count(*) FROM create_evidence").Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 0
			if mode == "generation-replacement" {
				want = 1
			}
			if count != want {
				t.Fatal("failed append escaped transaction", count)
			}
		})
	}
}

func TestCreateEvidenceConcurrentHandlesAndBlockedAdmission(t *testing.T) {
	f := newCreateEvidenceFixture(t, "unknown", true)
	proof := f.observe(t)
	if _, err := f.store.BeginReconciliation(context.Background()); err != nil {
		t.Fatal(err)
	}
	other, err := OpenSQLite(f.store.path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	var appended atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := []*SQLiteStore{f.store, other}[i%2]
			e, err := s.StoreCreateObservation(context.Background(), f.request, proof)
			if err != nil {
				t.Error(err)
			} else if !e.Replay {
				appended.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if appended.Load() != 1 || sqliteSnapshot(t, f.store).Ready || sqliteSnapshot(t, f.store).Allocated.Global.Runners != 1 || f.writes.Load() != 1 {
		t.Fatal("concurrent evidence changed charge/readiness or duplicated record")
	}
}

func TestCreateEvidenceBackupRestoreCLIAndHistoricalProvenance(t *testing.T) {
	f := newCreateEvidenceFixture(t, "confirmed", true)
	proof := f.observe(t)
	entry, err := f.store.StoreCreateObservation(context.Background(), f.request, proof)
	if err != nil {
		t.Fatal(err)
	}
	// Include permanent cancellation history in the SAME real evidence artifact.
	unused := request("unused")
	if _, err = f.store.Admit(context.Background(), unused); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.CancelUndispatched(context.Background(), unused); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "capacity-ledger")
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-buildvcs=false", "-o", binary, "../../cmd/capacity-ledger")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", output, err)
	}
	backup := filepath.Join(sqliteBackupDir(t), "backup.db")
	restore := filepath.Join(sqliteBackupDir(t), "restored.db")
	for _, args := range [][]string{{"backup", "--source", f.store.path, "--destination", backup}, {"restore", "--source", backup, "--destination", restore, "--old-writers-fenced"}} {
		if out, err := exec.Command(binary, args...).CombinedOutput(); err != nil {
			t.Fatalf("CLI: %s %v", out, err)
		}
	}
	for _, path := range []string{backup, restore} {
		s, err := OpenSQLite(path, Config{})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		got, err := s.LookupCreateEvidence(context.Background(), f.request.Key)
		if err != nil || !reflect.DeepEqual(got, entry) {
			t.Fatal("backup lost exact evidence", err)
		}
		state := sqliteSnapshot(t, s)
		if state.Ready || state.Allocated.Global.Runners != 1 || state.CancellationCount != 1 {
			t.Fatal("backup lost charges/tombstones or enabled admission")
		}
		binding, err := s.LookupHostBinding(context.Background(), "a")
		if err != nil || binding != f.binding {
			t.Fatal("backup lost original binding provenance", err)
		}
		if _, err = s.LookupCancellation(context.Background(), unused.Key); err != nil {
			t.Fatal("backup lost tombstone", err)
		}
		var version int
		if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != sqliteCurrentVersion {
			t.Fatal("backup wrong schema", version, err)
		}
	}
	// Exact original observed history survives v4->v5 migration; it isn't invented.
	if _, err = f.store.db.Exec("DROP TRIGGER create_evidence_immutable_update; DROP TRIGGER create_evidence_immutable_delete; DROP TABLE create_evidence; DROP TABLE ledger_lineage; PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	path := f.store.path
	f.store.Close()
	s, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.LookupCreateEvidence(context.Background(), f.request.Key); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("migration manufactured evidence", err)
	}
	if sqliteSnapshot(t, s).Allocated.Global.Runners != 1 {
		t.Fatal("migration erased charge")
	}
	if _, err = s.StoreCreateObservation(context.Background(), f.request, proof); err != nil {
		t.Fatal("qualified original v4 dispatch evidence refused", err)
	}
}

func TestCreateEvidenceCorruptOrNoncanonicalHistoryFailsClosed(t *testing.T) {
	for _, mode := range []string{"digest", "unknown-json", "duplicate-json", "indexed-scope", "record-version"} {
		t.Run(mode, func(t *testing.T) {
			f := newCreateEvidenceFixture(t, "confirmed", true)
			proof := f.observe(t)
			entry, err := f.store.StoreCreateObservation(context.Background(), f.request, proof)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.store.db.Exec("DROP TRIGGER create_evidence_immutable_update"); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "digest":
				if _, err = f.store.db.Exec("UPDATE create_evidence SET digest=?", strings.Repeat("f", 64)); err != nil {
					t.Fatal(err)
				}
			case "indexed-scope":
				if _, err = f.store.db.Exec("UPDATE create_evidence SET scope=?", strings.Repeat("f", 64)); err != nil {
					t.Fatal(err)
				}
			default:
				if mode == "unknown-json" {
					raw = append([]byte(`{"unknown":true,`), raw[1:]...)
				}
				if mode == "duplicate-json" {
					raw = append([]byte(`{"version":1,`), raw[1:]...)
				}
				if mode == "record-version" {
					entry.Version = 2
					raw, err = json.Marshal(entry)
					if err != nil {
						t.Fatal(err)
					}
				}
				if _, err = f.store.db.Exec("UPDATE create_evidence SET digest=?,record=?", evidenceDigest(raw), raw); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = f.store.LookupCreateEvidence(context.Background(), f.request.Key); !errors.Is(err, ErrCreateEvidence) {
				t.Fatal("corrupt history read as valid", err)
			}
			if _, err = f.store.StoreCreateObservation(context.Background(), f.request, proof); !errors.Is(err, ErrCreateEvidence) {
				t.Fatal("corrupt history overwritten", err)
			}
			if sqliteSnapshot(t, f.store).Allocated.Global.Runners != 1 || f.writes.Load() != 1 {
				t.Fatal("corruption released charge or redispatched")
			}
		})
	}
}

func TestCreateEvidenceMigrationNeverQualifiesUnboundV4Dispatch(t *testing.T) {
	f := newCreateEvidenceFixture(t, "confirmed", false)
	proof := f.observe(t)
	if _, err := f.store.db.Exec("DROP TRIGGER create_evidence_immutable_update; DROP TRIGGER create_evidence_immutable_delete; DROP TABLE create_evidence; DROP TABLE ledger_lineage; PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	path := f.store.path
	f.store.Close()
	s, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	record, err := s.LookupDispatch(context.Background(), f.request.Key)
	if err != nil || record.HostBindingSequence != 0 {
		t.Fatal("migration invented bound dispatch history", record, err)
	}
	if _, err := s.StoreCreateObservation(context.Background(), f.request, proof); !errors.Is(err, ErrCreateEvidence) {
		t.Fatal("unbound legacy proof blessed by migration", err)
	}
	if sqliteSnapshot(t, s).Allocated.Global.Runners != 1 || f.writes.Load() != 1 {
		t.Fatal("migration released or recreated an unknown allocation")
	}
}
