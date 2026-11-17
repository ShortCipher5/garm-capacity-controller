//go:build linux

package garmmanagement

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

// conditionalReceipt is the durable per-request-id record the fake keeps:
// the recorded outcome plus the exact payload it is bound to, so a replay
// attempt with different content yields the typed id-reuse refusal, like
// the server.
type conditionalReceipt struct {
	outcome garmapi.ReplayOutcome
	payload string
}

// fakeConditionalNative implements the subset of the native experimental
// conditional settings API the manager wiring uses: GET settings + strong
// ETag, PATCH honoring If-Match with a durable per-request-id receipt and
// replay, plus hooks to commit-then-drop a response or advance the revision
// externally (simulating another writer).
type fakeConditionalNative struct {
	mu       sync.Mutex
	settings garmapi.PoolSettings
	seq      int
	receipts map[string]conditionalReceipt
	// dropResponse commits (and optionally records the receipt) then sends a
	// truncated 200 - headers plus a partial body - and closes the connection.
	// The started response prevents transparent transport retries, and the
	// truncated body fails client-side decode: a deterministic lost-response.
	dropResponse, recordOnDrop bool
	// bumpNextPatch simulates a racing writer: it advances the revision just
	// before the precondition check of the next PATCH, forcing a 412.
	bumpNextPatch        bool
	sawIfMatch, sawIdem  string
	patchCalls, getCalls int
}

func newFakeConditionalNative() *fakeConditionalNative {
	return &fakeConditionalNative{settings: garmapi.PoolSettings{Priority: 0, MaxRunners: 4, MinIdleRunners: 1}, receipts: map[string]conditionalReceipt{}}
}

func (f *fakeConditionalNative) etag() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return "\"" + "1" + ":" + itoa(f.seq) + "\""
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

func (f *fakeConditionalNative) advanceLocked() {
	f.seq++
}

func (f *fakeConditionalNative) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.URL.Path, "/offline/entities/repository/") || !strings.HasSuffix(r.URL.Path, "/pools/"+poolID+"/settings") {
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	cur := "\"" + "1" + ":" + itoa(f.seq) + "\""
	f.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		f.mu.Lock()
		body, _ := json.Marshal(f.settings)
		f.mu.Unlock()
		w.Header().Set("ETag", cur)
		_, _ = w.Write(body)
	case http.MethodPatch:
		id := r.Header.Get("Idempotency-Key")
		f.mu.Lock()
		f.patchCalls++
		f.sawIdem = id
		f.sawIfMatch = r.Header.Get("If-Match")
		f.mu.Unlock()
		raw, _ := io.ReadAll(r.Body)
		var patch garmapi.PoolSettingsPatch
		_ = json.Unmarshal(raw, &patch)
		f.mu.Lock()
		receipt, replayed := f.receipts[id]
		f.mu.Unlock()
		if replayed {
			if receipt.payload != string(raw) {
				// The id is bound to different content, like the server's
				// id-reuse refusal; the client maps it to a typed 409.
				w.WriteHeader(http.StatusConflict)
				return
			}
			w.Header().Set("ETag", receipt.outcome.ETag)
			_ = json.NewEncoder(w).Encode(map[string]string{"outcome": receipt.outcome.Outcome, "resulting_etag": receipt.outcome.ETag})
			return
		}
		f.mu.Lock()
		if f.bumpNextPatch {
			f.bumpNextPatch = false
			f.advanceLocked()
			stale := "\"" + "1" + ":" + itoa(f.seq) + "\""
			f.mu.Unlock()
			w.Header().Set("ETag", stale)
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		f.mu.Unlock()
		if r.Header.Get("If-Match") != cur {
			w.Header().Set("ETag", cur)
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		f.mu.Lock()
		if patch.Priority != nil {
			f.settings.Priority = *patch.Priority
		}
		if patch.MaxRunners != nil {
			f.settings.MaxRunners = *patch.MaxRunners
		}
		if patch.MinIdleRunners != nil {
			f.settings.MinIdleRunners = *patch.MinIdleRunners
		}
		f.advanceLocked()
		next := "\"" + "1" + ":" + itoa(f.seq) + "\""
		if f.dropResponse {
			if f.recordOnDrop {
				f.receipts[id] = conditionalReceipt{outcome: garmapi.ReplayOutcome{Outcome: "applied_changed", ETag: next}, payload: string(raw)}
			}
			f.mu.Unlock()
			// Truncated 200: response already started (no transparent client
			// retry possible) and the body fails decode downstream.
			w.Header().Set("Connection", "close")
			w.Header().Set("ETag", next)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{\"prior"))
			return
		}
		f.receipts[id] = conditionalReceipt{outcome: garmapi.ReplayOutcome{Outcome: "applied_changed", ETag: next}, payload: string(raw)}
		body, _ := json.Marshal(f.settings)
		f.mu.Unlock()
		w.Header().Set("ETag", next)
		_, _ = w.Write(body)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeConditionalNative) currentSettings() garmapi.PoolSettings {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings
}

func conditionalFixture(t *testing.T, native *fakeConditionalNative) (*Manager, *Journal, *fakeRemote, *fakeConditionalNative, string) {
	t.Helper()
	server := httptest.NewTLSServer(native)
	t.Cleanup(server.Close)
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	conditional, err := garmapi.NewConditionalManagement(garmapi.ManagementConfig{
		Endpoint: server.URL, Token: "conditional-fixture-token", Trust: &tls.Config{RootCAs: pool},
		Providers: []string{"capacity"}, PoolIDs: []string{poolID}, RepositoryIDs: []string{repoID},
		ExperimentalConditionalSettings: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conditional.Close)
	path := filepath.Join(privateJournalDir(t), "journal.db")
	j, err := InitializeJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	f := remoteFixture()
	f.reflect = func() garmapi.ManagedPool {
		reflected := remoteFixture().pool
		current := native.currentSettings()
		reflected.Priority = current.Priority
		reflected.MaxRunners = current.MaxRunners
		reflected.MinIdleRunners = current.MinIdleRunners
		return reflected
	}
	m, err := NewConditional(f, j, conditional)
	if err != nil {
		t.Fatal(err)
	}
	return m, j, f, native, path
}

func TestConditionalPoolWriteUsesNativeConditionalAPI(t *testing.T) {
	native := newFakeConditionalNative()
	m, j, f, _, _ := conditionalFixture(t, native)
	priority := uint64(9)
	pool, err := m.UpdatePool(context.Background(), requestID, poolID, fingerprint, garmapi.PoolUpdate{Priority: &priority}, "conditional write")
	if err != nil {
		t.Fatal(err)
	}
	if pool.Priority != 9 {
		t.Fatalf("expected refreshed priority 9, got %d", pool.Priority)
	}
	if f.writes != 0 {
		t.Fatalf("legacy write path used %d times; conditional path expected", f.writes)
	}
	native.mu.Lock()
	defer native.mu.Unlock()
	if native.sawIfMatch == "" || !strings.HasPrefix(native.sawIfMatch, "\"") {
		t.Fatalf("native PATCH missing quoted If-Match: %q", native.sawIfMatch)
	}
	if native.sawIdem != requestID {
		t.Fatalf("native PATCH Idempotency-Key = %q, want journaled request id %q", native.sawIdem, requestID)
	}
	history, err := j.History(context.Background(), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) == 0 || history[len(history)-1].State != "confirmed" {
		t.Fatalf("journal history last state = %+v, want confirmed", history)
	}
}

func TestConditionalPoolWriteRefusesMinIdleDecreasePreDispatch(t *testing.T) {
	native := newFakeConditionalNative()
	m, _, f, nativeRef, _ := conditionalFixture(t, native)
	zero := uint64(0)
	_, err := m.UpdatePool(context.Background(), requestID, poolID, fingerprint, garmapi.PoolUpdate{MinIdleRunners: &zero}, "unsafe decrease")
	if !errors.Is(err, garmapi.ErrManagementUnsafe) {
		t.Fatalf("err = %v, want ErrManagementUnsafe", err)
	}
	nativeRef.mu.Lock()
	calls := nativeRef.patchCalls
	nativeRef.mu.Unlock()
	if calls != 0 {
		t.Fatalf("native PATCH called %d times for a refused decrease", calls)
	}
	if f.writes != 0 {
		t.Fatalf("legacy write path used for a tuple-only update: %d", f.writes)
	}
}

func TestPoolWriteWithTagsKeepsLegacyPath(t *testing.T) {
	native := newFakeConditionalNative()
	m, _, f, nativeRef, _ := conditionalFixture(t, native)
	priority := uint64(3)
	tags := []string{"linux", "gpu"}
	if _, err := m.UpdatePool(context.Background(), requestID, poolID, fingerprint, garmapi.PoolUpdate{Priority: &priority, Tags: &tags}, "tags stay legacy"); err != nil {
		t.Fatal(err)
	}
	if f.writes != 1 {
		t.Fatalf("legacy writes = %d, want 1 when tags are present", f.writes)
	}
	nativeRef.mu.Lock()
	defer nativeRef.mu.Unlock()
	if nativeRef.patchCalls != 0 {
		t.Fatalf("native PATCH called %d times for a tags-bearing update", nativeRef.patchCalls)
	}
}

func TestConditionalPoolWriteConflictIsRejected(t *testing.T) {
	native := newFakeConditionalNative()
	m, j, _, _, _ := conditionalFixture(t, native)
	native.bumpNextPatch = true // a racing writer moves the revision between our GET and PATCH
	priority := uint64(5)
	_, err := m.UpdatePool(context.Background(), requestID, poolID, fingerprint, garmapi.PoolUpdate{Priority: &priority}, "stale precondition")
	if !errors.Is(err, garmapi.ErrManagementConflict) {
		t.Fatalf("err = %v, want ErrManagementConflict", err)
	}
	history, herr := j.History(context.Background(), 0, 10)
	if herr != nil {
		t.Fatal(herr)
	}
	if len(history) == 0 || history[len(history)-1].State != "rejected" {
		t.Fatalf("journal history last state = %+v, want rejected", history)
	}
}

func TestConditionalUnknownResolvesFromDurableReceipt(t *testing.T) {
	native := newFakeConditionalNative()
	native.dropResponse = true
	native.recordOnDrop = true
	m, j, _, _, _ := conditionalFixture(t, native)
	priority := uint64(7)
	_, err := m.UpdatePool(context.Background(), requestID, poolID, fingerprint, garmapi.PoolUpdate{Priority: &priority}, "lost response")
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("err = %v, want ErrOutcomeUnknown", err)
	}
	pending, perr := j.Pending(context.Background())
	if perr != nil || len(pending) != 1 || pending[0].RequestID != requestID {
		t.Fatalf("pending after unknown = %v %v, want the single intent", pending, perr)
	}
	pool, rerr := m.ResolvePool(context.Background(), requestID)
	if rerr != nil {
		t.Fatalf("ResolvePool: %v", rerr)
	}
	if pool.Priority != 7 {
		t.Fatalf("resolved pool priority = %d, want 7", pool.Priority)
	}
	pending, _ = j.Pending(context.Background())
	if len(pending) != 0 {
		t.Fatalf("pending after resolve = %v, want none", pending)
	}
}

func TestResolvePoolAmbiguousOutcomeHoldsUnknown(t *testing.T) {
	native := newFakeConditionalNative()
	native.dropResponse = true // original dispatch never arrived (no receipt recorded)
	m, j, _, _, _ := conditionalFixture(t, native)
	priority := uint64(7)
	if _, err := m.UpdatePool(context.Background(), requestID, poolID, fingerprint, garmapi.PoolUpdate{Priority: &priority}, "never arrived"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("err = %v, want ErrOutcomeUnknown", err)
	}
	// Resolution dispatch is itself ambiguous: the intent must be retained,
	// never cleared from a stale or ambiguous observation.
	if _, err := m.ResolvePool(context.Background(), requestID); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("ResolvePool err = %v, want ErrOutcomeUnknown", err)
	}
	pending, _ := j.Pending(context.Background())
	if len(pending) != 1 {
		t.Fatalf("pending after ambiguous resolve = %v, want intent retained", pending)
	}
}

func TestResolvePoolWithoutPendingIntentConflicts(t *testing.T) {
	native := newFakeConditionalNative()
	m, _, _, _, _ := conditionalFixture(t, native)
	if _, err := m.ResolvePool(context.Background(), nextID); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("err = %v, want ErrRequestConflict", err)
	}
}

func TestConditionalWriteRejectsUnreviewedRevisionSkew(t *testing.T) {
	native := newFakeConditionalNative()
	m, _, f, nativeRef, _ := conditionalFixture(t, native)
	// The native revision carries state the operator never reviewed. Break
	// the fixture's legacy/native mirror so the reviewed legacy snapshot
	// (fingerprint-bound) still shows the old tuple while native moved on.
	f.reflect = nil
	native.mu.Lock()
	native.settings.Priority = 9
	native.mu.Unlock()
	priority := uint64(5)
	_, err := m.UpdatePool(context.Background(), requestID, poolID, fingerprint, garmapi.PoolUpdate{Priority: &priority}, "skewed revision")
	if !errors.Is(err, garmapi.ErrManagementConflict) {
		t.Fatalf("err = %v, want ErrManagementConflict", err)
	}
	nativeRef.mu.Lock()
	defer nativeRef.mu.Unlock()
	if nativeRef.patchCalls != 0 {
		t.Fatalf("native PATCH called %d times against an unreviewed revision", nativeRef.patchCalls)
	}
}

func TestResolvePoolRefusesLegacyUnknownWrite(t *testing.T) {
	native := newFakeConditionalNative()
	m, _, f, nativeRef, _ := conditionalFixture(t, native)
	f.writeFail = garmapi.ErrManagementOutcomeUnknown
	priority := uint64(4)
	tags := []string{"linux"}
	if _, err := m.UpdatePool(context.Background(), requestID, poolID, fingerprint, garmapi.PoolUpdate{Priority: &priority, Tags: &tags}, "legacy write"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("err = %v, want ErrOutcomeUnknown", err)
	}
	// A legacy write must never be resolved as a partial conditional write.
	if _, err := m.ResolvePool(context.Background(), requestID); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("ResolvePool err = %v, want ErrRequestConflict", err)
	}
	nativeRef.mu.Lock()
	defer nativeRef.mu.Unlock()
	if nativeRef.patchCalls != 0 {
		t.Fatalf("native PATCH called %d times while resolving a legacy write", nativeRef.patchCalls)
	}
}

func TestResolvePoolIdReuseHoldsUnknown(t *testing.T) {
	native := newFakeConditionalNative()
	native.dropResponse = true
	native.recordOnDrop = true
	m, j, _, _, _ := conditionalFixture(t, native)
	priority := uint64(7)
	if _, err := m.UpdatePool(context.Background(), requestID, poolID, fingerprint, garmapi.PoolUpdate{Priority: &priority}, "lost response"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("err = %v, want ErrOutcomeUnknown", err)
	}
	// The receipt now binds the id to different content (reconstruction
	// drift or foreign reuse): a 409 is NOT proof the original write
	// failed, so the intent must stay UNKNOWN.
	native.mu.Lock()
	rec := native.receipts[requestID]
	rec.payload = `{"priority":8}`
	native.receipts[requestID] = rec
	native.mu.Unlock()
	if _, err := m.ResolvePool(context.Background(), requestID); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("ResolvePool err = %v, want ErrOutcomeUnknown on id-reuse mismatch", err)
	}
	pending, _ := j.Pending(context.Background())
	if len(pending) != 1 {
		t.Fatalf("pending after id-reuse mismatch = %v, want intent retained", pending)
	}
}

func TestResolvePoolReappliesMinIdleGuardAgainstCurrentState(t *testing.T) {
	native := newFakeConditionalNative()
	native.dropResponse = true // original dispatch committed without a receipt
	m, _, _, nativeRef, _ := conditionalFixture(t, native)
	minIdle := uint64(3)
	if _, err := m.UpdatePool(context.Background(), requestID, poolID, fingerprint, garmapi.PoolUpdate{MinIdleRunners: &minIdle}, "increase min idle"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("err = %v, want ErrOutcomeUnknown", err)
	}
	// While the write was unknown, another writer raised min idle above the
	// journaled target and moved the revision. Resolution must hold UNKNOWN:
	// re-applying the journaled target now would decrease the current
	// minimum. The original precondition is stale, so the resolution
	// dispatch is refused with 412 before any settings change; without a
	// stored receipt there is no authoritative answer either way.
	native.mu.Lock()
	native.settings.MinIdleRunners = 4
	native.mu.Unlock()
	if _, err := m.ResolvePool(context.Background(), requestID); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("ResolvePool err = %v, want ErrOutcomeUnknown on safeguard re-check", err)
	}
	nativeRef.mu.Lock()
	calls := nativeRef.patchCalls
	nativeRef.mu.Unlock()
	if calls != 2 {
		t.Fatalf("native PATCH calls = %d, want the original dispatch plus the refused resolution attempt", calls)
	}
	if got := native.currentSettings().MinIdleRunners; got != 4 {
		t.Fatalf("native min idle after resolve = %d, want 4 (no re-application)", got)
	}
}

// TestResolvePoolConfirmsAppliedReceiptDespiteRaisedMinIdle covers the case
// where the original write committed (the durable receipt recorded it), the
// response was lost, and a later writer raised min idle above the journaled
// target before resolution. Confirming from the receipt changes nothing and
// must not be blocked by the minimum-idle policy re-check.
func TestResolvePoolConfirmsAppliedReceiptDespiteRaisedMinIdle(t *testing.T) {
	native := newFakeConditionalNative()
	native.dropResponse = true
	native.recordOnDrop = true
	m, j, _, _, _ := conditionalFixture(t, native)
	minIdle := uint64(3)
	if _, err := m.UpdatePool(context.Background(), requestID, poolID, fingerprint, garmapi.PoolUpdate{MinIdleRunners: &minIdle}, "lost response"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("err = %v, want ErrOutcomeUnknown", err)
	}
	// A later writer raises min idle above the journaled target and moves the
	// revision before anyone resolves the original write.
	native.mu.Lock()
	native.settings.MinIdleRunners = 4
	native.advanceLocked()
	native.mu.Unlock()
	pool, err := m.ResolvePool(context.Background(), requestID)
	if err != nil {
		t.Fatalf("ResolvePool: %v", err)
	}
	if pool.MinIdleRunners != 4 {
		t.Fatalf("resolved pool min idle = %d, want the current 4 (a replay must not re-apply the journaled 3)", pool.MinIdleRunners)
	}
	if got := native.currentSettings().MinIdleRunners; got != 4 {
		t.Fatalf("native min idle after resolve = %d, want 4 (replay must not overwrite the later writer)", got)
	}
	pending, _ := j.Pending(context.Background())
	if len(pending) != 0 {
		t.Fatalf("pending after resolve = %v, want none", pending)
	}
}

// TestResolvePoolFlagOffAfterRestartHoldsUnknown covers a restart with the
// experimental conditional client disabled while a conditional write is still
// pending: resolution has no receipt to consult and must hold UNKNOWN instead
// of reaching a nil client.
func TestResolvePoolFlagOffAfterRestartHoldsUnknown(t *testing.T) {
	native := newFakeConditionalNative()
	native.dropResponse = true
	native.recordOnDrop = true
	m, j, _, _, path := conditionalFixture(t, native)
	priority := uint64(7)
	if _, err := m.UpdatePool(context.Background(), requestID, poolID, fingerprint, garmapi.PoolUpdate{Priority: &priority}, "lost response"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("err = %v, want ErrOutcomeUnknown", err)
	}
	// Restart with the experiment disabled: production startup builds the
	// manager without a conditional client while the intent is still pending.
	lineage := j.Lineage()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenJournal(path, lineage)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, err := New(remoteFixture(), reopened)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ResolvePool(context.Background(), requestID); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("ResolvePool err = %v, want ErrOutcomeUnknown with the experimental client disabled", err)
	}
	pending, _ := reopened.Pending(context.Background())
	if len(pending) != 1 || pending[0].RequestID != requestID {
		t.Fatalf("pending after flag-off resolve = %v, want the intent retained", pending)
	}
}

func TestJournalMigratesV2ToV3DispatchColumn(t *testing.T) {
	dir := privateJournalDir(t)
	path := filepath.Join(dir, "journal.db")
	lineage := strings.Repeat("d", 64)
	// Rebuild the exact v2 journal shape (no dispatch column) from the schema
	// const, then prove OpenJournal migrates it in place and the new column
	// is fully usable.
	oldSchema := strings.ReplaceAll(schema, " dispatch_json BLOB,\n", "")
	oldSchema = strings.ReplaceAll(oldSchema, "PRAGMA user_version=3;", "PRAGMA user_version=2;")
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO journal_identity(id,lineage) VALUES(1,?)", lineage); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := OpenJournal(path, lineage)
	if err != nil {
		t.Fatalf("OpenJournal on a v2 journal: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })
	var version int
	if err := j.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 3 {
		t.Fatalf("user_version after migration = %d %v, want 3", version, err)
	}
	if _, err := j.begin(context.Background(), Intent{RequestID: requestID, Kind: "pool", Target: poolID, Reason: "migration", Before: []byte(`{}`), Desired: []byte(`{}`), Dispatch: []byte(`{"api":"legacy"}`)}, "hash", fingerprint); err != nil {
		t.Fatalf("begin with dispatch record: %v", err)
	}
	pending, err := j.Pending(context.Background())
	if err != nil || len(pending) != 1 || len(pending[0].Dispatch) == 0 {
		t.Fatalf("pending dispatch after migration = %v %v, want the recorded intent", pending, err)
	}
	if err := j.finish(context.Background(), requestID, "confirmed", []byte(`{}`)); err != nil {
		t.Fatalf("finish after migration: %v", err)
	}
}
