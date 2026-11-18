package managedsettings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

const poolID = "11111111-1111-4111-8111-111111111111"
const controllerID = "22222222-2222-4222-8222-222222222222"
const requestID = "33333333-3333-4333-8333-333333333333"

type fixtureState struct {
	sync.Mutex
	pool                                garmapi.ManagedPool
	source                              string
	writes                              int
	fail, slowRead, slowWrite, readOnly bool
	entered                             chan struct{}
}

func fixture(t *testing.T) (*Client, *fixtureState, Configuration) {
	t.Helper()
	s := &fixtureState{pool: garmapi.ManagedPool{ID: poolID, ProviderName: "capacity", MaxRunners: 10, MinIdleRunners: 1, Enabled: true, Fingerprint: strings.Repeat("a", 64), Tags: []string{"linux"}}, source: strings.Repeat("b", 64), entered: make(chan struct{}, 2)}
	server, trust, _ := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Lock()
		defer s.Unlock()
		if r.Header.Get("Authorization") != "" || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			t.Error("missing mTLS or leaked bearer")
		}
		w.Header().Set(managedsource.SourceHeader, s.source)
		w.Header().Set(managedsource.ControllerHeader, controllerID)
		if r.Method == "GET" && r.URL.Path == "/v1/managed/history" {
			json.NewEncoder(w).Encode(History{Events: []HistoryEvent{{RequestID: requestID, Kind: "pool", Target: poolID, State: "confirmed", Reason: "limit", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Before: s.pool, Desired: s.pool, Result: &s.pool}}})
			return
		}
		if r.Method == "GET" && r.URL.Path == "/v1/managed/status" {
			json.NewEncoder(w).Encode(garmmanagement.Status{Pending: []garmmanagement.Intent{}})
			return
		}
		if r.Method == "GET" && r.URL.Path == "/v1/managed/settings" {
			if s.slowRead {
				s.entered <- struct{}{}
				<-r.Context().Done()
				return
			}
			json.NewEncoder(w).Encode(garmmanagement.Snapshot{ManagementSnapshot: garmapi.ManagementSnapshot{Pools: []garmapi.ManagedPool{s.pool}, Repositories: []garmapi.ManagedRepository{}, WritesEnabled: !s.readOnly}, Status: garmmanagement.Status{Pending: []garmmanagement.Intent{}}})
			return
		}
		if r.Method == "POST" && r.URL.Path == "/v1/managed/pools/"+poolID+"/reduce-maximum" {
			s.writes++
			var body map[string]json.RawMessage
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 4 || body["request_id"] == nil || body["expected_fingerprint"] == nil || body["reason"] == nil {
				t.Error("unsafe request shape")
			}
			var target uint64
			json.Unmarshal(body["maximum"], &target)
			s.pool.MaxRunners = target
			s.pool.Fingerprint = strings.Repeat("c", 64)
			if s.slowWrite {
				s.entered <- struct{}{}
				<-r.Context().Done()
				return
			}
			if s.fail {
				w.WriteHeader(503)
				return
			}
			json.NewEncoder(w).Encode(s.pool)
			return
		}
		t.Errorf("unexpected route %s %s", r.Method, r.URL.Path)
		w.WriteHeader(404)
	}))
	hash := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
	config := Configuration{Endpoint: server.URL, ServerKeyPin: hex.EncodeToString(hash[:]), ControllerID: controllerID, TLS: trust, Providers: []string{"capacity"}, PoolIDs: []string{poolID}, AllowReductions: true}
	c, e := New(config)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(c.Close)
	return c, s, config
}
func TestActualManagedReductionAndNoBroadMutation(t *testing.T) {
	c, s, _ := fixture(t)
	ctx := context.Background()
	for _, n := range []uint64{0, 10, 11} {
		if _, e := c.ReduceMaximum(ctx, requestID, poolID, strings.Repeat("a", 64), n, "limit future starts"); e == nil {
			t.Fatal("unsafe maximum accepted")
		}
	}
	out, e := c.ReduceMaximum(ctx, requestID, poolID, strings.Repeat("a", 64), 2, "limit future starts")
	if e != nil || out.MaxRunners != 2 || out.MinIdleRunners != 1 || !out.Enabled {
		t.Fatal(out, e)
	}
	s.Lock()
	defer s.Unlock()
	if s.writes != 1 {
		t.Fatal(s.writes)
	}
}
func TestLostResponseLatchesUnknownWithoutReplay(t *testing.T) {
	c, s, _ := fixture(t)
	s.Lock()
	s.fail = true
	s.Unlock()
	if _, e := c.ReduceMaximum(context.Background(), requestID, poolID, strings.Repeat("a", 64), 2, "limit"); !errors.Is(e, garmapi.ErrManagementOutcomeUnknown) {
		t.Fatal(e)
	}
	status, e := c.Status(context.Background())
	if e != nil || !status.WriteBlocked {
		t.Fatal(status, e)
	}
	if _, e = c.ReduceMaximum(context.Background(), requestID, poolID, strings.Repeat("c", 64), 1, "never replay"); !errors.Is(e, garmapi.ErrManagementDisabled) {
		t.Fatal(e)
	}
	s.Lock()
	defer s.Unlock()
	if s.writes != 1 {
		t.Fatal(s.writes)
	}
}
func TestSourceDriftAndForeignPinRefuse(t *testing.T) {
	c, s, config := fixture(t)
	if _, e := c.Snapshot(context.Background()); e != nil {
		t.Fatal(e)
	}
	s.Lock()
	s.source = strings.Repeat("d", 64)
	s.Unlock()
	if _, e := c.Snapshot(context.Background()); e == nil {
		t.Fatal("foreign source accepted")
	}
	s.Lock()
	s.source = strings.Repeat("b", 64)
	s.Unlock()
	if _, e := c.Snapshot(context.Background()); e == nil {
		t.Fatal("drift did not latch")
	}
	config.ServerKeyPin = strings.Repeat("f", 64)
	foreign, e := New(config)
	if e != nil {
		t.Fatal(e)
	}
	defer foreign.Close()
	if _, e = foreign.Snapshot(context.Background()); e == nil {
		t.Fatal("wrong SPKI accepted")
	}
}
func TestCancellationSeparatesPreflightFromPossiblePost(t *testing.T) {
	for _, post := range []bool{false, true} {
		t.Run(map[bool]string{false: "preflight", true: "post"}[post], func(t *testing.T) {
			c, s, _ := fixture(t)
			s.Lock()
			s.slowRead = !post
			s.slowWrite = post
			s.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, e := c.ReduceMaximum(ctx, requestID, poolID, strings.Repeat("a", 64), 2, "limit")
				done <- e
			}()
			select {
			case <-s.entered:
			case <-ctx.Done():
				t.Fatal("request did not reach source")
			}
			cancel()
			e := <-done
			if post != errors.Is(e, garmapi.ErrManagementOutcomeUnknown) {
				t.Fatal("wrong dispatch classification", e)
			}
			s.Lock()
			defer s.Unlock()
			if s.writes != map[bool]int{false: 0, true: 1}[post] {
				t.Fatal(s.writes)
			}
		})
	}
}
func TestConcurrentReviewedReductionsCannotOverwrite(t *testing.T) {
	c, s, _ := fixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := c.ReduceMaximum(context.Background(), requestID, poolID, strings.Repeat("a", 64), 2, "limit")
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	success := 0
	for e := range errs {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal(success)
	}
	s.Lock()
	defer s.Unlock()
	if s.writes != 1 {
		t.Fatal(s.writes)
	}
}
func TestReadOnlyManagedClientCannotDispatch(t *testing.T) {
	_, s, config := fixture(t)
	config.AllowReductions = false
	c, e := New(config)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	snapshot, e := c.Snapshot(context.Background())
	if e != nil || snapshot.WritesEnabled {
		t.Fatal(e)
	}
	if _, e = c.ReduceMaximum(context.Background(), requestID, poolID, strings.Repeat("a", 64), 2, "must not write"); !errors.Is(e, garmapi.ErrManagementDisabled) {
		t.Fatal(e)
	}
	s.Lock()
	defer s.Unlock()
	if s.writes != 0 {
		t.Fatal("read mode sent mutation")
	}
}

func TestSupervisorReadOnlyStateRefusesReduction(t *testing.T) {
	c, s, _ := fixture(t)
	s.Lock()
	s.readOnly = true
	s.Unlock()
	if _, e := c.ReduceMaximum(context.Background(), requestID, poolID, strings.Repeat("a", 64), 2, "must not write"); !errors.Is(e, garmapi.ErrManagementDisabled) {
		t.Fatal(e)
	}
	s.Lock()
	defer s.Unlock()
	if s.writes != 0 {
		t.Fatal("disabled supervisor received POST")
	}
}

func TestConfirmedHistoryNeverClearsUnknown(t *testing.T) {
	c, s, _ := fixture(t)
	s.Lock()
	s.fail = true
	s.Unlock()
	if _, err := c.ReduceMaximum(context.Background(), requestID, poolID, strings.Repeat("a", 64), 2, "limit"); !errors.Is(err, garmapi.ErrManagementOutcomeUnknown) {
		t.Fatal(err)
	}
	h, err := c.History(context.Background())
	if err != nil || len(h.Events) != 1 {
		t.Fatal(h, err)
	}
	status, err := c.Status(context.Background())
	if err != nil || !status.WriteBlocked {
		t.Fatal(status, err)
	}
	if _, err = c.ReduceMaximum(context.Background(), requestID, poolID, strings.Repeat("c", 64), 1, "no replay"); !errors.Is(err, garmapi.ErrManagementDisabled) {
		t.Fatal(err)
	}
	s.Lock()
	defer s.Unlock()
	if s.writes != 1 {
		t.Fatal(s.writes)
	}
}
