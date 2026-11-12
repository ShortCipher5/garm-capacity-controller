package gateway

import (
	"bytes"
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
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	common "github.com/cloudbase/garm-provider-common/params"
)

func automaticEvidenceTLS(t *testing.T, handler http.Handler) (*httptest.Server, *tls.Config) {
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

const automaticUUID = "11111111-1111-4111-8111-111111111111"
const automaticGeneration = "22222222-2222-4222-8222-222222222222"
const automaticOtherGeneration = "33333333-3333-4333-8333-333333333333"

type automaticEvidenceFixture struct {
	facade                        *Facade
	store                         *admission.SQLiteStore
	client                        *incusapi.Client
	inspect                       *sql.DB
	path                          string
	mode                          string
	posts, waits, terminal, named atomic.Int32
	mu                            sync.Mutex
	configs                       map[string]map[string]string
	createdAt                     string
	cancel                        context.CancelFunc
}

type cancelAfterVerifiedBackend struct {
	*incusapi.Client
	calls  atomic.Int32
	cancel context.CancelFunc
}

func (b *cancelAfterVerifiedBackend) Instance(ctx context.Context, name string) (incusapi.Instance, error) {
	value, err := b.Client.Instance(ctx, name)
	if err == nil && b.calls.Add(1) == 1 {
		b.cancel()
	}
	return value, err
}

func automaticFixture(t *testing.T, mode string, cancel context.CancelFunc) *automaticEvidenceFixture {
	t.Helper()
	f := &automaticEvidenceFixture{mode: mode, configs: map[string]map[string]string{}, cancel: cancel}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	identityDir := t.TempDir()
	if err := os.Chmod(identityDir, 0700); err != nil {
		t.Fatal(err)
	}
	f.path = filepath.Join(dir, "ledger.db")
	identityPath := filepath.Join(identityDir, "runtime.identity")
	budget := admission.Resources{CPU: 4000, RAM: 8192, Runners: 4}
	config := admission.Config{Global: budget, Hosts: map[string]admission.Host{"host-a": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool-a": {Budget: budget, Hosts: []string{"host-a"}}}, MaxObservationAge: time.Minute}
	if err := admission.InitializeSQLite(f.path, identityPath, config); err != nil {
		t.Fatal(err)
	}
	var err error
	f.store, err = admission.OpenRuntimeSQLite(f.path, identityPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.store.Close() })
	f.inspect, err = sql.Open("sqlite3", f.path+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	f.inspect.SetMaxOpenConns(1)
	t.Cleanup(func() { f.inspect.Close() })
	if err = f.inspect.Ping(); err != nil {
		t.Fatal(err)
	}
	write := func(w http.ResponseWriter, value any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "sync", "status_code": 200, "metadata": value})
	}
	var server *httptest.Server
	var credentials *tls.Config
	server, credentials = automaticEvidenceTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			t.Error("client not authenticated")
		}
		if r.URL.Query().Get("project") != "runners" {
			t.Error("wrong bounded project")
		}
		if r.Method != http.MethodGet {
			f.posts.Add(1)
			if r.Method != http.MethodPost || r.URL.Path != "/1.0/instances" {
				t.Error("unexpected backend write")
				http.Error(w, "forbidden", 500)
				return
			}
			var body struct {
				Name   string
				Config map[string]string
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				http.Error(w, "bad request", 500)
				return
			}
			f.mu.Lock()
			f.configs[body.Name] = body.Config
			f.createdAt = time.Now().UTC().Format(time.RFC3339Nano)
			f.mu.Unlock()
			if mode == "lost-post" {
				http.Error(w, "private-lost-create-response", 500)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "async", "status_code": 100, "operation": "/1.0/operations/" + operation + "?project=runners", "metadata": map[string]any{"id": operation}})
			return
		}
		switch r.URL.Path {
		case "/1.0":
			leaf := server.Certificate()
			fingerprint := sha256.Sum256(leaf.Raw)
			write(w, map[string]any{"api_version": "1.0", "auth": "trusted", "api_extensions": []string{"instance_create_start"}, "environment": map[string]any{"server_clustered": false, "server_name": "automatic-evidence-daemon", "server_version": "6.0.4", "certificate": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})), "certificate_fingerprint": fmt.Sprintf("%x", fingerprint)}})
		case "/1.0/instances":
			write(w, []any{})
		case "/1.0/operations":
			write(w, map[string]any{})
		case "/1.0/operations/" + operation + "/wait":
			f.waits.Add(1)
			record, err := f.store.LookupDispatch(context.Background(), key("controller-a", "pool-a", "runner-1"))
			if err != nil || record.State != "submitted" || record.OperationID != operation {
				t.Errorf("wait preceded durable submission: %+v %v", record, err)
			}
			status := 200
			if mode == "wait-failure" {
				status = 400
			}
			write(w, map[string]any{"id": operation, "status_code": status, "resources": map[string][]string{"instances": {"/1.0/instances/runner-1?project=runners"}}})
		case "/1.0/instances/runner-1/state":
			write(w, map[string]any{"status_code": 103})
		case "/1.0/instances/runner-1":
			count := f.named.Add(1)
			if mode == "identity-read-failure" && count == 2 {
				http.Error(w, "private-read-error-body", 500)
				return
			}
			f.mu.Lock()
			cfg := map[string]string{}
			for k, v := range f.configs["runner-1"] {
				cfg[k] = v
			}
			created := f.createdAt
			f.mu.Unlock()
			if len(cfg) == 0 {
				http.NotFound(w, r)
				return
			}
			cfg["volatile.uuid"] = automaticUUID
			cfg["volatile.uuid.generation"] = automaticGeneration
			if mode == "generation-drift" && count >= 3 {
				cfg["volatile.uuid.generation"] = automaticOtherGeneration
			}
			write(w, map[string]any{"name": "runner-1", "status_code": 103, "created_at": created, "expanded_config": cfg})
		case "/1.0/operations/" + operation:
			f.terminal.Add(1)
			record, err := f.store.LookupDispatch(context.Background(), key("controller-a", "pool-a", "runner-1"))
			if err != nil || record.State != "submitted" || record.OperationID != operation {
				t.Errorf("capture not original submitted callback: %+v %v", record, err)
			}
			if mode == "expired" {
				http.Error(w, "private-expired-operation", 404)
				return
			}
			if mode == "slow" {
				<-r.Context().Done()
				return
			}
			if mode == "lineage-lost" {
				if _, err := f.inspect.Exec("UPDATE ledger_lineage SET runtime_kind='archive' WHERE id=1"); err != nil {
					t.Error(err)
				}
			}
			body := map[string]any{"id": operation, "class": "task", "description": "Creating instance", "status_code": 200, "status": "Success", "err": "", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "resources": map[string][]string{"instances": {"/1.0/instances/runner-1?project=runners"}}, "metadata": map[string]any{"private": "private-operation-metadata"}}
			if mode == "malformed" {
				delete(body, "err")
			}
			if mode == "wrong-operation" {
				body["id"] = automaticOtherGeneration
			}
			write(w, body)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	f.client, err = incusapi.New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.client.Close)
	var backend Backend = f.client
	if cancel != nil {
		backend = &cancelAfterVerifiedBackend{Client: f.client, cancel: cancel}
	}
	policy := PoolPolicy{Host: "host-a", CPU: 1000, RAM: 1024, Flavor: "bounded-small", Image: "pinned-linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"runner-base"}, Type: "container", Arch: common.Amd64}
	f.facade, err = New(context.Background(), f.store, "controller-a", map[string]PoolPolicy{"pool-a": policy}, map[string]Backend{"host-a": backend})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.facade.Reconcile(context.Background(), garmRows{}); err != nil {
		t.Fatal(err)
	}
	source, actor := configureSourceFixture(t, f.facade)
	preflight(t, f.facade, "runner-1", "pool-a")
	publishOriginalFixture(t, f.facade, source, actor, "runner-1")
	return f
}

func (f *automaticEvidenceFixture) rawState(t *testing.T) (string, int, int) {
	t.Helper()
	var state string
	var reservations, evidence int
	if err := f.inspect.QueryRow("SELECT state FROM dispatches WHERE reservation_key=?", key("controller-a", "pool-a", "runner-1")).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := f.inspect.QueryRow("SELECT count(*) FROM reservations").Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if err := f.inspect.QueryRow("SELECT count(*) FROM create_evidence").Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	return state, reservations, evidence
}

func captureEvidenceLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	prior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(prior) })
	return &output
}

func TestAutomaticCreateEvidenceActualTLSCapturedBeforeConfirmationAndNoReplay(t *testing.T) {
	logs := captureEvidenceLogs(t)
	f := automaticFixture(t, "success", nil)
	result, err := f.facade.Create(context.Background(), bootstrap("runner-1", "pool-a"))
	if err != nil || result.Status != common.InstanceRunning {
		t.Fatalf("create %+v %v", result, err)
	}
	reqKey := key("controller-a", "pool-a", "runner-1")
	record, err := f.store.LookupCreateEvidence(context.Background(), reqKey)
	if err != nil || record.Observation.OperationID != operation || record.Observation.Identity.Subject.UUID != automaticUUID || record.HostBindingSequence <= 0 {
		t.Fatalf("missing real evidence %+v %v", record, err)
	}
	state, charged, evidence := f.rawState(t)
	if state != "confirmed" || charged != 1 || evidence != 1 || f.posts.Load() != 1 || f.waits.Load() != 1 || f.terminal.Load() != 1 {
		t.Fatal("capture altered lifecycle/accounting or repeated work", state, charged, evidence)
	}
	snapshot, err := f.store.ReadSnapshot(context.Background())
	if err != nil || snapshot.Allocated.Global.Runners != 1 {
		t.Fatal("capture released capacity", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := f.facade.Create(context.Background(), bootstrap("runner-1", "pool-a")); err != nil {
			t.Fatal("confirmed replay failed", err)
		}
	}
	after, err := f.store.LookupCreateEvidence(context.Background(), reqKey)
	if err != nil || after != record || f.posts.Load() != 1 || f.waits.Load() != 1 || f.terminal.Load() != 1 {
		t.Fatal("replay recaptured/rewrote/recreated", err)
	}
	if logs.Len() != 0 {
		t.Fatal("successful capture emitted a missing-proof diagnostic", logs.String())
	}
	var body []byte
	if err := f.inspect.QueryRow("SELECT record FROM create_evidence").Scan(&body); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"bootstrap-secret", "private-operation-metadata", "https://", "PRIVATE KEY"} {
		if strings.Contains(string(body), secret) {
			t.Fatal("raw secret in evidence")
		}
	}
}

func TestAutomaticCreateEvidenceOrdinaryFailureKeepsVerifiedGuestAndCharge(t *testing.T) {
	for _, mode := range []string{"expired", "malformed", "wrong-operation", "identity-read-failure", "generation-drift", "audit-refusal", "slow"} {
		t.Run(mode, func(t *testing.T) {
			logs := captureEvidenceLogs(t)
			f := automaticFixture(t, mode, nil)
			if mode == "audit-refusal" {
				if _, err := f.inspect.Exec("CREATE TRIGGER reject_auto_evidence BEFORE INSERT ON audit WHEN NEW.kind='create_observation_recorded' BEGIN SELECT RAISE(ABORT,'private-audit-error'); END"); err != nil {
					t.Fatal(err)
				}
			}
			began := time.Now()
			result, err := f.facade.Create(context.Background(), bootstrap("runner-1", "pool-a"))
			if err != nil || result.Status != common.InstanceRunning {
				t.Fatalf("optional history failure changed verified create %+v %v", result, err)
			}
			if elapsed := time.Since(began); elapsed > 8*time.Second {
				t.Fatal("collector exceeded overall bound", elapsed)
			}
			state, charged, evidence := f.rawState(t)
			if state != "confirmed" || charged != 1 || evidence != 0 || f.posts.Load() != 1 || f.waits.Load() != 1 {
				t.Fatal("failed capture promoted proof/released/recreated", state, charged, evidence)
			}
			if _, err := f.store.LookupCreateEvidence(context.Background(), key("controller-a", "pool-a", "runner-1")); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("missing proof fabricated", err)
			}
			text := logs.String()
			if strings.Count(text, createEvidenceDiagnostic) != 1 || strings.Contains(text, "private-") || strings.Contains(text, "bootstrap-secret") || strings.Contains(text, operation) {
				t.Fatal("diagnostic missing/repeated/raw", text)
			}
			before := f.terminal.Load()
			if _, err := f.facade.Create(context.Background(), bootstrap("runner-1", "pool-a")); err != nil {
				t.Fatal(err)
			}
			if f.posts.Load() != 1 || f.terminal.Load() != before || strings.Count(logs.String(), createEvidenceDiagnostic) != 1 {
				t.Fatal("replay retried capture or create")
			}
		})
	}
}

func TestAutomaticCreateEvidenceNeverRunsOnUnknownFailedCreate(t *testing.T) {
	for _, mode := range []string{"lost-post", "wait-failure"} {
		t.Run(mode, func(t *testing.T) {
			logs := captureEvidenceLogs(t)
			f := automaticFixture(t, mode, nil)
			if _, err := f.facade.Create(context.Background(), bootstrap("runner-1", "pool-a")); err == nil {
				t.Fatal("failed create confirmed")
			}
			state, charged, evidence := f.rawState(t)
			if state != "unknown" || charged != 1 || evidence != 0 || f.posts.Load() != 1 || f.terminal.Load() != 0 || logs.Len() != 0 {
				t.Fatal("unknown outcome captured/invented/released", state, charged, evidence, logs.String())
			}
			record, err := f.store.LookupDispatch(context.Background(), key("controller-a", "pool-a", "runner-1"))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "wait-failure" && record.OperationID != operation {
				t.Fatal("known submitted operation lost")
			}
			if mode == "lost-post" && record.OperationID != "" {
				t.Fatal("lost operation invented")
			}
			if _, err := f.facade.Create(context.Background(), bootstrap("runner-1", "pool-a")); err == nil || f.posts.Load() != 1 || f.terminal.Load() != 0 {
				t.Fatal("unknown create replayed")
			}
		})
	}
}

func TestAutomaticCreateEvidenceIndependentCaptureAfterCallerDisconnect(t *testing.T) {
	logs := captureEvidenceLogs(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := automaticFixture(t, "success", cancel)
	if _, err := f.facade.Create(ctx, bootstrap("runner-1", "pool-a")); err == nil {
		t.Fatal("cancelled final read unexpectedly succeeded")
	}
	state, charged, evidence := f.rawState(t)
	if state != "confirmed" || charged != 1 || evidence != 1 || f.posts.Load() != 1 || f.terminal.Load() != 1 || logs.Len() != 0 {
		t.Fatal("disconnect erased bounded completed-create evidence", state, charged, evidence, logs.String())
	}
}

func TestAutomaticCreateEvidenceUncertainCommitAndLineageLossFailClosed(t *testing.T) {
	for _, mode := range []string{"commit-uncertain", "lineage-lost"} {
		t.Run(mode, func(t *testing.T) {
			logs := captureEvidenceLogs(t)
			f := automaticFixture(t, mode, nil)
			if mode == "commit-uncertain" {
				if _, err := f.inspect.Exec(`CREATE TABLE auto_fault_parent(id INTEGER PRIMARY KEY); CREATE TABLE auto_fault(value INTEGER REFERENCES auto_fault_parent(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER reject_auto_commit AFTER INSERT ON create_evidence BEGIN INSERT INTO auto_fault VALUES(42); END;`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.facade.Create(context.Background(), bootstrap("runner-1", "pool-a")); err == nil {
				t.Fatal("unavailable writer confirmed")
			}
			state, charged, evidence := f.rawState(t)
			if state != "submitted" || charged != 1 || evidence != 0 || f.posts.Load() != 1 || f.terminal.Load() != 1 {
				t.Fatal("writer failure erased known charge/correlation", state, charged, evidence)
			}
			var op string
			if err := f.inspect.QueryRow("SELECT operation_id FROM dispatches").Scan(&op); err != nil || op != operation {
				t.Fatal("known submitted UUID lost", op, err)
			}
			_, err := f.store.ReadSnapshot(context.Background())
			want := admission.ErrUncertain
			if mode == "lineage-lost" {
				want = admission.ErrRuntimeLineage
			}
			if !errors.Is(err, want) {
				t.Fatal("writer did not fail closed", err)
			}
			if _, err := f.facade.Create(context.Background(), bootstrap("runner-1", "pool-a")); err == nil || f.posts.Load() != 1 {
				t.Fatal("blocked writer retried create")
			}
			if text := logs.String(); strings.Count(text, createEvidenceDiagnostic) != 1 || strings.Contains(text, "private-") || strings.Contains(text, "FOREIGN KEY") {
				t.Fatal("raw/incorrect persistence diagnostic", text)
			}
		})
	}
}
