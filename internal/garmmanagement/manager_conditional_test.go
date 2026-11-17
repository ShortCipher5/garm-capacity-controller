//go:build linux

package garmmanagement

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

// fakeConditionalNative implements the subset of the native experimental
// conditional settings API the manager wiring uses: GET settings + strong
// ETag, PATCH honoring If-Match with a durable per-request-id receipt and
// replay, plus hooks to commit-then-drop a response or advance the revision
// externally (simulating another writer).
type fakeConditionalNative struct {
	mu       sync.Mutex
	settings garmapi.PoolSettings
	seq      int
	receipts map[string]garmapi.ReplayOutcome // request id -> recorded outcome
	// dropResponse commits (and optionally records the receipt) then sends a
	// truncated 200 - headers plus a partial body - and closes the connection.
	// The started response prevents transparent transport retries, and the
	// truncated body fails client-side decode: a deterministic lost-response.
	dropResponse, recordOnDrop bool
	// bumpNextPatch simulates a racing writer: it advances the revision just
	// before the precondition check of the next PATCH, forcing a 412.
	bumpNextPatch       bool
	sawIfMatch, sawIdem string
	patchCalls          int
}

func newFakeConditionalNative() *fakeConditionalNative {
	return &fakeConditionalNative{settings: garmapi.PoolSettings{Priority: 0, MaxRunners: 4, MinIdleRunners: 1}, receipts: map[string]garmapi.ReplayOutcome{}}
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
		replay, replayed := f.receipts[id]
		f.mu.Unlock()
		if replayed {
			w.Header().Set("ETag", replay.ETag)
			_ = json.NewEncoder(w).Encode(map[string]string{"outcome": replay.Outcome, "resulting_etag": replay.ETag})
			return
		}
		var patch garmapi.PoolSettingsPatch
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &patch)
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
				f.receipts[id] = garmapi.ReplayOutcome{Outcome: "applied_changed", ETag: next}
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
		f.receipts[id] = garmapi.ReplayOutcome{Outcome: "applied_changed", ETag: next}
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

func conditionalFixture(t *testing.T, native *fakeConditionalNative) (*Manager, *Journal, *fakeRemote, *fakeConditionalNative) {
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
	return m, j, f, native
}

func TestConditionalPoolWriteUsesNativeConditionalAPI(t *testing.T) {
	native := newFakeConditionalNative()
	m, j, f, _ := conditionalFixture(t, native)
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
	m, _, f, nativeRef := conditionalFixture(t, native)
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
	m, _, f, nativeRef := conditionalFixture(t, native)
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
	m, j, _, _ := conditionalFixture(t, native)
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
	m, j, _, _ := conditionalFixture(t, native)
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
	m, j, _, _ := conditionalFixture(t, native)
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
	m, _, _, _ := conditionalFixture(t, native)
	if _, err := m.ResolvePool(context.Background(), nextID); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("err = %v, want ErrRequestConflict", err)
	}
}
