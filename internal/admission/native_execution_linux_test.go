package admission

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

type nativeFixture struct {
	store       *SQLiteStore
	binding     NativeDomainBinding
	observation nativeidentity.Observation
	owner       NativeOwnerAuthority
	controller  NativeOwnerAuthority
}

// Reconstruct a genuine pre-native schema in historical migration fixtures.
const nativeExecutionDowngradeSQL = `
DROP TRIGGER native_outcome_requires_consumption;
DROP TRIGGER native_owner_not_stock_origin;
DROP TRIGGER native_owner_not_preparation;
DROP TABLE native_execution_consumptions;
DROP TABLE native_executions;
DROP TABLE native_domain_bindings;
DELETE FROM audit WHERE kind IN ('native_domain_enrolled','native_execution_bound','native_execution_consumed','schema_migrated_v19');
`

func nativeExecutionFixture(t *testing.T) nativeFixture {
	t.Helper()
	ctx := context.Background()
	config := fixture(time.Now())
	s := sqliteFixtureStore(t, config, false)
	fence, bindings := bindingFixture(t, s)
	var descriptor nativeidentity.Descriptor
	authority := make(chan NativeOwnerAuthority, 2)
	server, controllerTLS, ownerTLS := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/authenticate-test" {
			a, e := AuthenticateNativeOwner(r.TLS)
			if e != nil {
				t.Error(e)
			}
			authority <- a
			w.WriteHeader(204)
			return
		}
		if r.URL.Path != nativeidentity.Path {
			t.Error("wrong path")
		}
		json.NewEncoder(w).Encode(descriptor)
	}))
	domain := strings.Repeat("a", 64)
	descriptor = nativeidentity.Descriptor{DomainID: domain, PolicyDigest: strings.Repeat("b", 64), OwnerServerLeaf: nativeidentity.Fingerprint(server.TLS.Certificates[0].Certificate[0]), OwnerClientLeaf: nativeidentity.Fingerprint(ownerTLS.Certificates[0].Certificate[0]), NativeServerKey: strings.Repeat("c", 64), NativeOrigin: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain)), NativeScope: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain + "\x00default")), NativeServerName: "private-native-domain", NativeServerVersion: "6.0.4"}
	observation, e := nativeidentity.Observe(ctx, nativeidentity.Configuration{Endpoint: server.URL, TLS: controllerTLS, OwnerServerLeaf: descriptor.OwnerServerLeaf, OwnerClientLeaf: descriptor.OwnerClientLeaf, PolicyDigest: descriptor.PolicyDigest, DomainID: domain})
	if e != nil {
		t.Fatal(e)
	}
	measured, e := ObservedNativeHostBinding("a", observation)
	if e != nil {
		t.Fatal(e)
	}
	for i, b := range bindings {
		if b.Host == "a" {
			bindings[i] = measured
		}
	}
	if e := s.BindHostIdentities(ctx, fence, bindings); e != nil {
		t.Fatal(e)
	}
	bound, e := s.LookupHostBinding(ctx, "a")
	if e != nil {
		t.Fatal(e)
	}
	spec := NativeDomainSpec{Controller: "controller", Host: "a", HostBindingSequence: bound.Sequence, DomainID: domain, PolicyDigest: descriptor.PolicyDigest, OwnerClientLeaf: descriptor.OwnerClientLeaf, OwnerServerLeaf: descriptor.OwnerServerLeaf}
	enrolled, e := s.EnrollNativeDomain(ctx, spec, observation)
	if e != nil {
		t.Fatal(e)
	}
	if e := s.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(config, s.now())}); e != nil {
		t.Fatal(e)
	}
	authenticate := func(c *tls.Config) NativeOwnerAuthority {
		transport := &http.Transport{TLSClientConfig: c, DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport}
		r, e := client.Get(server.URL + "/authenticate-test")
		if e != nil {
			t.Fatal(e)
		}
		r.Body.Close()
		return <-authority
	}
	return nativeFixture{s, enrolled, observation, authenticate(ownerTLS), authenticate(controllerTLS)}
}
func nativeEnvelope(t *testing.T, f nativeFixture, p DispatchPermit) (NativeExecutionEnvelope, string) {
	t.Helper()
	e, err := f.store.BindNativeExecution(context.Background(), p, f.binding.Sequence, strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := NativeEnvelopeDigest(e)
	if err != nil {
		t.Fatal(err)
	}
	return e, digest
}

func TestNativeEnrollmentRequiresLiveExactNativeIdentity(t *testing.T) {
	f := nativeExecutionFixture(t)
	ctx := context.Background()
	s := f.store
	if _, e := s.EnrollNativeDomain(ctx, f.binding.Spec, nativeidentity.Observation{}); !errors.Is(e, ErrNativeExecution) {
		t.Fatal("caller proof accepted", e)
	}
	for _, field := range []string{"host", "domain", "policy", "client", "server", "binding"} {
		spec := f.binding.Spec
		switch field {
		case "host":
			spec.Host = "b"
		case "domain":
			spec.DomainID = strings.Repeat("f", 64)
		case "policy":
			spec.PolicyDigest = strings.Repeat("f", 64)
		case "client":
			spec.OwnerClientLeaf = strings.Repeat("f", 64)
		case "server":
			spec.OwnerServerLeaf = strings.Repeat("f", 64)
		case "binding":
			spec.HostBindingSequence++
		}
		if _, e := s.EnrollNativeDomain(ctx, spec, f.observation); e == nil {
			t.Fatal("changed enrollment", field)
		}
	}
	replay, e := s.EnrollNativeDomain(ctx, f.binding.Spec, f.observation)
	if e != nil || replay != f.binding {
		t.Fatal("enrollment replay changed", e)
	}
	for _, q := range []string{"UPDATE native_domain_bindings SET host='other'", "DELETE FROM native_domain_bindings"} {
		if _, e := s.db.Exec(q); e == nil {
			t.Fatal("mutable enrollment")
		}
	}
}
func TestNativeClaimIsOneShotAndCanonicalChargeUnchanged(t *testing.T) {
	f := nativeExecutionFixture(t)
	s := f.store
	ctx := context.Background()
	plan, b := stockPlan(t, s, "native-runner")
	var calls int
	_, e := s.ExecuteStockProviderDispatch(ctx, plan, b, stockAuthority(), stockDigest(), func(ctx context.Context, p DispatchPermit) (DispatchReceipt, error) {
		envelope, digest := nativeEnvelope(t, f, p)
		if envelope.Resources != plan.Request.Resources || envelope.NativeName != plan.Execution.Name {
			t.Fatal("envelope invented identity")
		}
		if _, e := s.ConsumeNativeExecution(ctx, f.controller, p.ID(), digest); !errors.Is(e, ErrNativeExecution) {
			t.Fatal("wrong role accepted", e)
		}
		if _, e := s.ConsumeNativeExecution(ctx, f.owner, p.ID(), strings.Repeat("f", 64)); !errors.Is(e, ErrNativeExecution) {
			t.Fatal("wrong body accepted", e)
		}
		if _, e := s.CancelUndispatched(ctx, plan.Request); !errors.Is(e, ErrDispatchClaimed) {
			t.Fatal("cancelled native intent", e)
		}
		claim, e := s.ConsumeNativeExecution(ctx, f.owner, p.ID(), digest)
		if e != nil || claim.ClaimID == "" || claim.EnvelopeDigest != digest {
			t.Fatal("claim failed", e)
		}
		calls++
		if _, e := s.ConsumeNativeExecution(ctx, f.owner, p.ID(), digest); !errors.Is(e, ErrNativeConsumed) {
			t.Fatal("replay authorized", e)
		}
		return DispatchReceipt{State: "confirmed", OperationID: "11111111-1111-4111-8111-111111111111", ResourceID: plan.Execution.Name}, nil
	})
	if e != nil || calls != 1 {
		t.Fatal(e, calls)
	}
	if sqliteSnapshot(t, s).Allocated.Global.Runners != 1 {
		t.Fatal("claim changed charge")
	}
	if e := sqliteCheckNativeExecutions(ctx, s); e != nil {
		t.Fatal("canonical chain invalid", e)
	}
}
func TestNativeLostClaimAcknowledgementSurvivesRestartAndBackup(t *testing.T) {
	f := nativeExecutionFixture(t)
	s := f.store
	ctx := context.Background()
	p, b := stockPlan(t, s, "lost-ack")
	var permit, digest string
	_, e := s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), func(ctx context.Context, d DispatchPermit) (DispatchReceipt, error) {
		_, digest = nativeEnvelope(t, f, d)
		permit = d.ID()
		if _, e := s.ConsumeNativeExecution(ctx, f.owner, permit, digest); e != nil {
			t.Fatal(e)
		}
		return DispatchReceipt{}, errors.New("lost claim ACK")
	})
	if !errors.Is(e, ErrDispatchUnknown) {
		t.Fatal(e)
	}
	reopened, e := OpenSQLite(s.path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if _, e := reopened.ConsumeNativeExecution(ctx, f.owner, permit, digest); !errors.Is(e, ErrNativeConsumed) {
		t.Fatal("restart reauthorized", e)
	}
	backup := filepath.Join(sqliteBackupDir(t), "native.db")
	if e := s.BackupSQLite(ctx, backup); e != nil {
		t.Fatal(e)
	}
	restored := filepath.Join(sqliteBackupDir(t), "restored.db")
	if e := RestoreSQLite(ctx, backup, restored); e != nil {
		t.Fatal(e)
	}
	copy, e := OpenSQLite(restored, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer copy.Close()
	if _, e := copy.ConsumeNativeExecution(ctx, f.owner, permit, digest); !errors.Is(e, ErrNativeConsumed) {
		t.Fatal("restore reauthorized", e)
	}
	if sqliteSnapshot(t, copy).Allocated.Global.Runners != 1 {
		t.Fatal("unknown charge lost")
	}
}
func TestNativeConcurrentClaimsAcrossHandlesOnlyOneFreshReceipt(t *testing.T) {
	f := nativeExecutionFixture(t)
	s := f.store
	ctx := context.Background()
	p, b := stockPlan(t, s, "racing")
	other, e := OpenSQLite(s.path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	_, e = s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), func(ctx context.Context, p DispatchPermit) (DispatchReceipt, error) {
		_, digest := nativeEnvelope(t, f, p)
		var successes atomic.Int32
		var wg sync.WaitGroup
		begin := make(chan struct{})
		for i := 0; i < 16; i++ {
			store := s
			if i%2 == 0 {
				store = other
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-begin
				if _, e := store.ConsumeNativeExecution(ctx, f.owner, p.ID(), digest); e == nil {
					successes.Add(1)
				} else if !errors.Is(e, ErrNativeConsumed) {
					t.Error(e)
				}
			}()
		}
		close(begin)
		wg.Wait()
		if successes.Load() != 1 {
			t.Fatal("fresh receipts", successes.Load())
		}
		return DispatchReceipt{}, errors.New("no physical operation in test")
	})
	if !errors.Is(e, ErrDispatchUnknown) {
		t.Fatal(e)
	}
}
func TestNativeRefusesRebindingUnknownAndUnconsumedOutcome(t *testing.T) {
	f := nativeExecutionFixture(t)
	s := f.store
	ctx := context.Background()
	p, b := stockPlan(t, s, "bound")
	var permit, digest string
	_, e := s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), func(ctx context.Context, d DispatchPermit) (DispatchReceipt, error) {
		_, digest = nativeEnvelope(t, f, d)
		permit = d.ID()
		if _, e := s.BindNativeExecution(ctx, d, f.binding.Sequence, strings.Repeat("e", 64)); !errors.Is(e, ErrConflict) {
			t.Fatal("body rebound", e)
		}
		if e := s.RecordDispatchOutcome(ctx, d, DispatchReceipt{State: "submitted", OperationID: "11111111-1111-4111-8111-111111111111", ResourceID: p.Execution.Name}); e == nil {
			t.Fatal("unconsumed native outcome accepted")
		}
		return DispatchReceipt{}, errors.New("uncertain delivery")
	})
	if !errors.Is(e, ErrDispatchUnknown) {
		t.Fatal(e)
	}
	if _, e := s.ConsumeNativeExecution(ctx, f.owner, permit, digest); !errors.Is(e, ErrNativeExecution) {
		t.Fatal("unknown dispatch authorized", e)
	}
}

func TestNativeMigrationFrom18RetainsChargesWithoutInventingAuthority(t *testing.T) {
	s := placementFixture(t)
	p, _ := stockPlan(t, s, "migration18")
	if _, e := s.db.Exec(nativeExecutionDowngradeSQL + "PRAGMA user_version=18"); e != nil {
		t.Fatal(e)
	}
	path := s.path
	s.Close()
	migrated, e := OpenSQLite(path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer migrated.Close()
	snap := sqliteSnapshot(t, migrated)
	if snap.Ready || snap.Allocated.Global.Runners != 1 {
		t.Fatalf("migration changed charges/readiness: %+v", snap)
	}
	var n int
	for _, table := range []string{"native_domain_bindings", "native_executions", "native_execution_consumptions"} {
		if e := migrated.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); e != nil || n != 0 {
			t.Fatalf("invented native authority %s %d %v", table, n, e)
		}
	}
	replay, e := migrated.AdmitStockProviderPlacement(context.Background(), placementRequest("migration18"), stockAuthority(), stockDigest())
	if e != nil || !replay.Replay || replay.Request != p.Request {
		t.Fatal("stock replay changed", e)
	}
}

func TestNativeMissingConsumedHistoryFailsClosed(t *testing.T) {
	f := nativeExecutionFixture(t)
	s := f.store
	ctx := context.Background()
	p, b := stockPlan(t, s, "missing-consumption")
	var id, digest string
	_, e := s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), func(ctx context.Context, d DispatchPermit) (DispatchReceipt, error) {
		id = d.ID()
		_, digest = nativeEnvelope(t, f, d)
		if _, e := s.ConsumeNativeExecution(ctx, f.owner, id, digest); e != nil {
			t.Fatal(e)
		}
		return DispatchReceipt{}, errors.New("uncertain")
	})
	if !errors.Is(e, ErrDispatchUnknown) {
		t.Fatal(e)
	}
	if _, e := s.db.Exec("DROP TRIGGER native_consumption_no_delete; DELETE FROM native_execution_consumptions"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ConsumeNativeExecution(ctx, f.owner, id, digest); !errors.Is(e, ErrNativeConsumed) {
		t.Fatal("missing row reauthorized", e)
	}
	if e := sqliteCheckNativeExecutions(ctx, s); e == nil {
		t.Fatal("missing consumption accepted")
	}
	if reopened, e := OpenSQLite(s.path, Config{}); e == nil {
		reopened.Close()
		t.Fatal("startup accepted missing consumption")
	}
	if e := s.BackupSQLite(ctx, filepath.Join(sqliteBackupDir(t), "corrupt-native.db")); e == nil {
		t.Fatal("backup accepted missing consumption")
	}
}

func TestNativeConsumeAuditFailureRollsBackWithoutReturningAuthority(t *testing.T) {
	f := nativeExecutionFixture(t)
	s := f.store
	ctx := context.Background()
	p, b := stockPlan(t, s, "audit-rollback")
	_, e := s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), func(ctx context.Context, d DispatchPermit) (DispatchReceipt, error) {
		_, digest := nativeEnvelope(t, f, d)
		if _, e := s.db.Exec("CREATE TRIGGER fail_native_consume BEFORE INSERT ON audit WHEN NEW.kind='native_execution_consumed' BEGIN SELECT RAISE(ABORT,'injected audit failure'); END"); e != nil {
			t.Fatal(e)
		}
		receipt, e := s.ConsumeNativeExecution(ctx, f.owner, d.ID(), digest)
		if e == nil || receipt.ClaimID != "" {
			t.Fatal("returned authority before durable audit", e)
		}
		var n int
		if e := s.db.QueryRow("SELECT count(*) FROM native_execution_consumptions").Scan(&n); e != nil || n != 0 {
			t.Fatal("partial consumption", n, e)
		}
		if _, e := s.db.Exec("DROP TRIGGER fail_native_consume"); e != nil {
			t.Fatal(e)
		}
		if _, e := s.ConsumeNativeExecution(ctx, f.owner, d.ID(), digest); e != nil {
			t.Fatal("rolled-back claim could not be consumed", e)
		}
		return DispatchReceipt{}, errors.New("native outcome unknown")
	})
	if !errors.Is(e, ErrDispatchUnknown) {
		t.Fatal(e)
	}
}

func TestNativePreConsumeBackupCannotReauthorizeAfterRestoredReconciliation(t *testing.T) {
	f := nativeExecutionFixture(t)
	s := f.store
	ctx := context.Background()
	p, b := stockPlan(t, s, "pre-claim-backup")
	backup := filepath.Join(sqliteBackupDir(t), "pre-consume.db")
	var id, digest string
	_, e := s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), func(ctx context.Context, d DispatchPermit) (DispatchReceipt, error) {
		id = d.ID()
		_, digest = nativeEnvelope(t, f, d)
		if e := s.BackupSQLite(ctx, backup); e != nil {
			t.Fatal(e)
		}
		if _, e := s.ConsumeNativeExecution(ctx, f.owner, id, digest); e != nil {
			t.Fatal(e)
		}
		return DispatchReceipt{}, errors.New("outcome unknown after consumption")
	})
	if !errors.Is(e, ErrDispatchUnknown) {
		t.Fatal(e)
	}
	restored := filepath.Join(sqliteBackupDir(t), "restored-preconsume.db")
	if e := RestoreSQLite(ctx, backup, restored); e != nil {
		t.Fatal(e)
	}
	copy, e := OpenSQLite(restored, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer copy.Close()
	fence, e := copy.BeginReconciliation(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e := copy.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(sqliteSnapshot(t, copy).Config, copy.now())}); e != nil {
		t.Fatal(e)
	}
	if _, e := copy.ConsumeNativeExecution(ctx, f.owner, id, digest); !errors.Is(e, ErrFence) {
		t.Fatal("old backup renewed native authority", e)
	}
	if sqliteSnapshot(t, copy).Allocated.Global.Runners != 1 {
		t.Fatal("charge lost after restore")
	}
}
