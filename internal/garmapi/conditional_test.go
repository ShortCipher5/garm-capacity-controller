package garmapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type conditionalFixture struct {
	mu          sync.Mutex
	priority    uint64
	maxRunners  uint64
	minIdle     uint64
	epoch       uint64
	seq         uint64
	gets        int
	patches     int
	replays     int
	ifMatches   []string
	idempotency []string
	bodies      []map[string]json.RawMessage
	appliedByID map[string]conditionalApplied
	patchStatus int // nonzero: PATCH answers this status with the current ETag instead of applying
	omitETag    bool
	badETag     bool
	badBody     bool
}

type conditionalApplied struct {
	payloadHash string
	etag        string
}

func (f *conditionalFixture) etag() string {
	return strconv.FormatUint(f.epoch, 10) + ":" + strconv.FormatUint(f.seq, 10)
}

func (f *conditionalFixture) writeLocked(w http.ResponseWriter) {
	if f.badBody {
		_, _ = w.Write([]byte(`{"priority":"high"}`))
		return
	}
	if !f.omitETag {
		if f.badETag {
			w.Header().Set("ETag", `W/"weak"`)
		} else {
			w.Header().Set("ETag", strconv.Quote(f.etag()))
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"priority": f.priority, "max_runners": f.maxRunners, "min_idle_runners": f.minIdle})
}

func (f *conditionalFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer separate-write-fixture-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	want := "/api/v1/offline/entities/" + EntityKindRepository + "/" + managedRepoID + "/pools/" + managedPoolID + "/settings"
	if r.URL.Path != want {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		f.gets++
		f.writeLocked(w)
	case http.MethodPatch:
		f.patches++
		f.ifMatches = append(f.ifMatches, r.Header.Get("If-Match"))
		key := r.Header.Get("Idempotency-Key")
		f.idempotency = append(f.idempotency, key)
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		rawPayload, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		payloadHash := fmt.Sprintf("%x", sha256.Sum256(rawPayload))
		if stored, ok := f.appliedByID[key]; ok {
			if stored.payloadHash == payloadHash {
				// Replay: answer from the stored outcome without applying
				// or advancing, mirroring the native receipt journal.
				f.replays++
				w.Header().Set("ETag", stored.etag)
				_ = json.NewEncoder(w).Encode(map[string]any{"outcome": "applied_changed", "resulting_etag": stored.etag})
				return
			}
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "Idempotency-Key already used with a different payload or pool"})
			return
		}
		body := map[string]json.RawMessage{}
		if err := json.Unmarshal(rawPayload, &body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.bodies = append(f.bodies, body)
		if f.patchStatus != 0 {
			// Rejections carry the current ETag so 412 callers can rebase.
			f.seq++
			w.Header().Set("ETag", strconv.Quote(f.etag()))
			w.WriteHeader(f.patchStatus)
			return
		}
		if raw, ok := body["priority"]; ok {
			_ = json.Unmarshal(raw, &f.priority)
		}
		if raw, ok := body["max_runners"]; ok {
			_ = json.Unmarshal(raw, &f.maxRunners)
		}
		if raw, ok := body["min_idle_runners"]; ok {
			_ = json.Unmarshal(raw, &f.minIdle)
		}
		f.seq++
		if key != "" {
			if f.appliedByID == nil {
				f.appliedByID = map[string]conditionalApplied{}
			}
			f.appliedByID[key] = conditionalApplied{payloadHash: payloadHash, etag: strconv.Quote(f.etag())}
		}
		f.writeLocked(w)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func conditionalServer(t *testing.T, mutate func(*conditionalFixture)) (*ConditionalManagementClient, *conditionalFixture) {
	t.Helper()
	f := &conditionalFixture{maxRunners: 4, minIdle: 1, epoch: 7, seq: 3}
	if mutate != nil {
		mutate(f)
	}
	server, trust := fixture(t, f)
	client, err := NewConditionalManagement(ManagementConfig{Endpoint: server.URL, Token: "separate-write-fixture-token", Trust: trust, Providers: []string{"capacity"}, PoolIDs: []string{managedPoolID}, RepositoryIDs: []string{managedRepoID}, ExperimentalConditionalSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client, f
}

func TestConditionalSettingsETagRoundTrip(t *testing.T) {
	c, f := conditionalServer(t, nil)
	ref := EntityRef{Kind: EntityKindRepository, ID: managedRepoID}
	got, err := c.GetConditionalSettings(context.Background(), ref, managedPoolID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ETag != `"7:3"` || got.Settings.Priority != 0 || got.Settings.MaxRunners != 4 || got.Settings.MinIdleRunners != 1 {
		t.Fatalf("get %+v", got)
	}
	res, err := c.PatchConditionalSettings(context.Background(), ref, managedPoolID, got.ETag, "request-1", PoolSettingsPatch{MaxRunners: value(uint64(9)), MinIdleRunners: value(uint64(2))})
	if err != nil {
		t.Fatal(err)
	}
	if res.ETag != `"7:4"` || res.Settings.MaxRunners != 9 || res.Settings.MinIdleRunners != 2 || res.Settings.Priority != 0 {
		t.Fatalf("patch %+v", res)
	}
	if _, err = c.PatchConditionalSettings(context.Background(), ref, managedPoolID, res.ETag, "", PoolSettingsPatch{Priority: value(uint64(1))}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gets != 1 || f.patches != 2 || len(f.ifMatches) != 2 || len(f.bodies) != 2 {
		t.Fatalf("accounting gets=%d patches=%d", f.gets, f.patches)
	}
	if f.ifMatches[0] != `"7:3"` || f.ifMatches[1] != `"7:4"` {
		t.Fatalf("If-Match not round-tripped: %v", f.ifMatches)
	}
	if f.idempotency[0] != "request-1" || f.idempotency[1] != "" {
		t.Fatalf("optional Idempotency-Key wrong: %v", f.idempotency)
	}
	if len(f.bodies[0]) != 2 {
		t.Fatalf("strict body not a subset: %v", f.bodies[0])
	}
	if _, ok := f.bodies[0]["max_runners"]; !ok {
		t.Fatalf("patched field missing: %v", f.bodies[0])
	}
	if len(f.bodies[1]) != 1 || f.bodies[1]["priority"] == nil {
		t.Fatalf("second strict body wrong: %v", f.bodies[1])
	}
}

func TestConditionalSettingsConflictExposesFreshETag(t *testing.T) {
	c, f := conditionalServer(t, func(f *conditionalFixture) { f.patchStatus = http.StatusPreconditionFailed })
	ref := EntityRef{Kind: EntityKindRepository, ID: managedRepoID}
	got, err := c.GetConditionalSettings(context.Background(), ref, managedPoolID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.PatchConditionalSettings(context.Background(), ref, managedPoolID, got.ETag, "", PoolSettingsPatch{MaxRunners: value(uint64(2))})
	if !errors.Is(err, ErrManagementConflict) {
		t.Fatalf("412 is not a management conflict: %v", err)
	}
	var conflict *SettingsConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("fresh ETag not typed: %v", err)
	}
	if conflict.FreshETag != `"7:4"` || conflict.FreshETag == got.ETag {
		t.Fatalf("stale ETag surfaced: %+v", conflict)
	}
	if f.patches != 1 {
		t.Fatal("conflict retried")
	}
}

func TestConditionalSettingsTypedRejections(t *testing.T) {
	cases := map[int]error{
		http.StatusBadRequest:           ErrConditionalSettingsInvalid,
		http.StatusForbidden:            ErrConditionalSettingsForbidden,
		http.StatusNotFound:             ErrConditionalSettingsNotFound,
		http.StatusUnprocessableEntity:  ErrConditionalSettingsUnprocessable,
		http.StatusPreconditionRequired: ErrConditionalSettingsPreconditionRequired,
	}
	for status, want := range cases {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c, f := conditionalServer(t, func(f *conditionalFixture) { f.patchStatus = status })
			ref := EntityRef{Kind: EntityKindRepository, ID: managedRepoID}
			got, err := c.GetConditionalSettings(context.Background(), ref, managedPoolID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.PatchConditionalSettings(context.Background(), ref, managedPoolID, got.ETag, "", PoolSettingsPatch{Priority: value(uint64(1))})
			if !errors.Is(err, want) {
				t.Fatalf("status %d mapped to %v", status, err)
			}
			if errors.Is(err, ErrManagementOutcomeUnknown) || errors.Is(err, ErrManagementConflict) {
				t.Fatalf("rejection masquerading as ambiguous write: %v", err)
			}
			if f.patches != 1 {
				t.Fatal("rejection retried")
			}
		})
	}
}

func TestConditionalSettingsPatchOutcomeUnknownNeverRetried(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c, f := conditionalServer(t, func(f *conditionalFixture) { f.patchStatus = status })
			ref := EntityRef{Kind: EntityKindRepository, ID: managedRepoID}
			got, err := c.GetConditionalSettings(context.Background(), ref, managedPoolID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.PatchConditionalSettings(context.Background(), ref, managedPoolID, got.ETag, "", PoolSettingsPatch{Priority: value(uint64(3))})
			if !errors.Is(err, ErrManagementOutcomeUnknown) {
				t.Fatalf("status %d: %v", status, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.patches != 1 {
				t.Fatal("ambiguous write retried")
			}
		})
	}
}

func TestConditionalSettingsTimeoutSemantics(t *testing.T) {
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	c, err := NewConditionalManagement(ManagementConfig{Endpoint: server.URL, Token: "separate-write-fixture-token", Trust: trust, Providers: []string{"capacity"}, PoolIDs: []string{managedPoolID}, RepositoryIDs: []string{managedRepoID}, ExperimentalConditionalSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ref := EntityRef{Kind: EntityKindRepository, ID: managedRepoID}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err = c.PatchConditionalSettings(ctx, ref, managedPoolID, `"7:3"`, "", PoolSettingsPatch{Priority: value(uint64(1))}); !errors.Is(err, ErrManagementOutcomeUnknown) {
		t.Fatalf("write timeout is not outcome-unknown: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err = c.GetConditionalSettings(ctx, ref, managedPoolID); !errors.Is(err, ErrManagement) || errors.Is(err, ErrManagementOutcomeUnknown) {
		t.Fatalf("read timeout: %v", err)
	}
}

func TestConditionalSettingsMalformedResponsesFailClosed(t *testing.T) {
	ref := EntityRef{Kind: EntityKindRepository, ID: managedRepoID}
	t.Run("get-missing-etag", func(t *testing.T) {
		c, f := conditionalServer(t, func(f *conditionalFixture) { f.omitETag = true })
		if _, err := c.GetConditionalSettings(context.Background(), ref, managedPoolID); err == nil {
			t.Fatal("etag-less settings accepted")
		}
		if f.gets != 1 {
			t.Fatal("read accounting wrong")
		}
	})
	t.Run("get-weak-etag", func(t *testing.T) {
		c, _ := conditionalServer(t, func(f *conditionalFixture) { f.badETag = true })
		if _, err := c.GetConditionalSettings(context.Background(), ref, managedPoolID); err == nil {
			t.Fatal("weak ETag accepted")
		}
	})
	t.Run("get-malformed-body", func(t *testing.T) {
		c, _ := conditionalServer(t, func(f *conditionalFixture) { f.badBody = true })
		if _, err := c.GetConditionalSettings(context.Background(), ref, managedPoolID); err == nil {
			t.Fatal("malformed settings body accepted")
		}
	})
	t.Run("get-inconsistent-values", func(t *testing.T) {
		c, _ := conditionalServer(t, func(f *conditionalFixture) { f.maxRunners, f.minIdle = 2, 5 })
		if _, err := c.GetConditionalSettings(context.Background(), ref, managedPoolID); err == nil {
			t.Fatal("min_idle above max accepted")
		}
	})
	t.Run("patch-malformed-body", func(t *testing.T) {
		c, f := conditionalServer(t, func(f *conditionalFixture) { f.badBody = true })
		if _, err := c.PatchConditionalSettings(context.Background(), ref, managedPoolID, `"7:3"`, "", PoolSettingsPatch{Priority: value(uint64(1))}); !errors.Is(err, ErrManagementOutcomeUnknown) {
			t.Fatalf("committed-but-unreadable write: %v", err)
		}
		if f.patches != 1 {
			t.Fatal("ambiguous write retried")
		}
	})
	t.Run("patch-missing-etag", func(t *testing.T) {
		c, _ := conditionalServer(t, func(f *conditionalFixture) { f.omitETag = true })
		if _, err := c.PatchConditionalSettings(context.Background(), ref, managedPoolID, `"7:3"`, "", PoolSettingsPatch{Priority: value(uint64(1))}); !errors.Is(err, ErrManagementOutcomeUnknown) {
			t.Fatalf("committed-but-etagless write: %v", err)
		}
	})
	t.Run("patch-weak-etag", func(t *testing.T) {
		c, _ := conditionalServer(t, func(f *conditionalFixture) { f.badETag = true })
		if _, err := c.PatchConditionalSettings(context.Background(), ref, managedPoolID, `"7:3"`, "", PoolSettingsPatch{Priority: value(uint64(1))}); !errors.Is(err, ErrManagementOutcomeUnknown) {
			t.Fatalf("committed-but-weak-etag write: %v", err)
		}
	})
}

func TestConditionalSettingsRejectsMalformedInputsWithoutDispatch(t *testing.T) {
	c, f := conditionalServer(t, nil)
	ref := EntityRef{Kind: EntityKindRepository, ID: managedRepoID}
	valid := PoolSettingsPatch{Priority: value(uint64(1))}
	cases := map[string]struct {
		ref     EntityRef
		pool    string
		etag    string
		request string
		patch   PoolSettingsPatch
	}{
		"bad-kind":       {EntityRef{Kind: "pools", ID: managedRepoID}, managedPoolID, `"7:3"`, "", valid},
		"bad-entity-id":  {EntityRef{Kind: EntityKindRepository, ID: "not-a-uuid"}, managedPoolID, `"7:3"`, "", valid},
		"bad-pool-id":    {ref, "not-a-uuid", `"7:3"`, "", valid},
		"unscoped-pool":  {ref, otherID, `"7:3"`, "", valid},
		"unscoped-repo":  {EntityRef{Kind: EntityKindRepository, ID: otherID}, managedPoolID, `"7:3"`, "", valid},
		"unquoted-etag":  {ref, managedPoolID, "7:3", "", valid},
		"weak-etag":      {ref, managedPoolID, `W/"7:3"`, "", valid},
		"garbage-etag":   {ref, managedPoolID, `"seven:three"`, "", valid},
		"empty-patch":    {ref, managedPoolID, `"7:3"`, "", PoolSettingsPatch{}},
		"priority-cap":   {ref, managedPoolID, `"7:3"`, "", PoolSettingsPatch{Priority: value(uint64(1000001))}},
		"max-zero":       {ref, managedPoolID, `"7:3"`, "", PoolSettingsPatch{MaxRunners: value(uint64(0))}},
		"max-cap":        {ref, managedPoolID, `"7:3"`, "", PoolSettingsPatch{MaxRunners: value(uint64(1000001))}},
		"bad-request-id": {ref, managedPoolID, `"7:3"`, "bad\nid", valid},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := c.PatchConditionalSettings(context.Background(), tc.ref, tc.pool, tc.etag, tc.request, tc.patch); !errors.Is(err, ErrManagementUnsafe) {
				t.Fatalf("malformed input accepted: %v", err)
			}
		})
	}
	if _, err := c.GetConditionalSettings(context.Background(), EntityRef{Kind: "x", ID: managedRepoID}, managedPoolID); !errors.Is(err, ErrManagementUnsafe) {
		t.Fatalf("bad read kind accepted: %v", err)
	}
	if _, err := c.GetConditionalSettings(context.Background(), ref, otherID); !errors.Is(err, ErrManagementUnsafe) {
		t.Fatalf("unscoped read accepted: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gets != 0 || f.patches != 0 {
		t.Fatalf("malformed input dispatched: gets=%d patches=%d", f.gets, f.patches)
	}
}

func TestConditionalSettingsDisabledByDefault(t *testing.T) {
	server, trust := fixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("disabled config made network request") }))
	config := ManagementConfig{Endpoint: server.URL, Token: "separate-write-fixture-token", Trust: trust, Providers: []string{"capacity"}, PoolIDs: []string{managedPoolID}, RepositoryIDs: []string{managedRepoID}}
	if _, err := NewConditionalManagement(config); !errors.Is(err, ErrConditionalManagementDisabled) {
		t.Fatalf("client constructed without opt-in: %v", err)
	}
	// Existing construction is unchanged in both flag states.
	m, err := NewManagement(config)
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	config.ExperimentalConditionalSettings = true
	m, err = NewManagement(config)
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	c, err := NewConditionalManagement(config)
	if err != nil {
		t.Fatalf("opt-in constructor refused: %v", err)
	}
	c.Close()
}

func TestConditionalManagementInvalidConfiguration(t *testing.T) {
	server, trust := fixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid config made network request") }))
	base := ManagementConfig{Endpoint: server.URL, Token: "test", Trust: trust, Providers: []string{"capacity"}, PoolIDs: []string{managedPoolID}, RepositoryIDs: []string{managedRepoID}, ExperimentalConditionalSettings: true}
	many := make([]string, 1001)
	for i := range many {
		many[i] = fmt.Sprintf("00000000-0000-4000-8000-%012x", i+1)
	}
	for name, config := range map[string]ManagementConfig{
		"no-pools":            {Endpoint: base.Endpoint, Token: base.Token, Trust: base.Trust, Providers: base.Providers, RepositoryIDs: []string{managedRepoID}, ExperimentalConditionalSettings: true},
		"invalid-pool":        {Endpoint: base.Endpoint, Token: base.Token, Trust: base.Trust, Providers: base.Providers, PoolIDs: []string{"invalid"}, ExperimentalConditionalSettings: true},
		"duplicate-pools":     {Endpoint: base.Endpoint, Token: base.Token, Trust: base.Trust, Providers: base.Providers, PoolIDs: []string{managedPoolID, managedPoolID}, ExperimentalConditionalSettings: true},
		"uppercase-pool":      {Endpoint: base.Endpoint, Token: base.Token, Trust: base.Trust, Providers: base.Providers, PoolIDs: []string{"ABCDEF00-0000-4000-8000-000000000001"}, ExperimentalConditionalSettings: true},
		"too-many-ids":        {Endpoint: base.Endpoint, Token: base.Token, Trust: base.Trust, Providers: base.Providers, PoolIDs: many, ExperimentalConditionalSettings: true},
		"invalid-repository":  {Endpoint: base.Endpoint, Token: base.Token, Trust: base.Trust, Providers: base.Providers, PoolIDs: []string{managedPoolID}, RepositoryIDs: []string{"invalid"}, ExperimentalConditionalSettings: true},
		"duplicate-repo-refs": {Endpoint: base.Endpoint, Token: base.Token, Trust: base.Trust, Providers: base.Providers, PoolIDs: []string{managedPoolID}, RepositoryIDs: []string{managedRepoID, managedRepoID}, ExperimentalConditionalSettings: true},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewConditionalManagement(config); !errors.Is(err, ErrManagement) {
				t.Fatalf("invalid config accepted: %v", err)
			}
		})
	}
	// The gate wins over every other validation when the opt-in is missing.
	gated := base
	gated.ExperimentalConditionalSettings = false
	gated.PoolIDs = []string{"invalid"}
	if _, err := NewConditionalManagement(gated); !errors.Is(err, ErrConditionalManagementDisabled) {
		t.Fatalf("gate ordering wrong: %v", err)
	}
}

func TestConditionalSettingsReplayUsesStoredOutcome(t *testing.T) {
	c, f := conditionalServer(t, nil)
	ref := EntityRef{Kind: EntityKindRepository, ID: managedRepoID}
	got, err := c.GetConditionalSettings(context.Background(), ref, managedPoolID)
	if err != nil {
		t.Fatal(err)
	}
	want := got.Settings.Priority + 1
	first, err := c.PatchConditionalSettings(context.Background(), ref, managedPoolID, got.ETag, "replay-key-1", PoolSettingsPatch{Priority: &want})
	if err != nil {
		t.Fatal(err)
	}
	// Re-presenting the same (key, payload) — even with the now-stale
	// original ETag — is answered from the durable receipt, not re-applied.
	replay, err := c.PatchConditionalSettings(context.Background(), ref, managedPoolID, got.ETag, "replay-key-1", PoolSettingsPatch{Priority: &want})
	if err != nil {
		t.Fatalf("replay held as failure: %v", err)
	}
	if replay.ETag != first.ETag {
		t.Fatalf("replay advanced the revision: %q vs %q", replay.ETag, first.ETag)
	}
	if replay.Settings != (PoolSettings{}) {
		t.Fatalf("replay invented a settings tuple: %+v", replay.Settings)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.replays != 1 {
		t.Fatalf("fixture did not serve from receipt: replays=%d", f.replays)
	}
	if strconv.Quote(f.etag()) != first.ETag {
		t.Fatalf("replay mutated state: etag now %q", f.etag())
	}
}

func TestConditionalSettingsRequestIDReuseIsTyped(t *testing.T) {
	c, f := conditionalServer(t, nil)
	ref := EntityRef{Kind: EntityKindRepository, ID: managedRepoID}
	got, err := c.GetConditionalSettings(context.Background(), ref, managedPoolID)
	if err != nil {
		t.Fatal(err)
	}
	one := got.Settings.Priority + 1
	if _, err = c.PatchConditionalSettings(context.Background(), ref, managedPoolID, got.ETag, "reuse-key-1", PoolSettingsPatch{Priority: &one}); err != nil {
		t.Fatal(err)
	}
	two := one + 1
	_, err = c.PatchConditionalSettings(context.Background(), ref, managedPoolID, got.ETag, "reuse-key-1", PoolSettingsPatch{Priority: &two})
	if !errors.Is(err, ErrConditionalSettingsRequestIDReuse) {
		t.Fatalf("id reuse is not typed: %v", err)
	}
	if errors.Is(err, ErrManagementOutcomeUnknown) || errors.Is(err, ErrManagementConflict) {
		t.Fatalf("definitive refusal masquerading as ambiguous/conflict: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.patches != 2 {
		t.Fatalf("reuse accounting wrong: patches=%d", f.patches)
	}
	if current := strconv.Quote(f.etag()); current != `"7:4"` {
		t.Fatalf("refused request mutated the revision: %s", current)
	}
}

func TestConditionalReplayDecodeRejectsNonAppliedOutcomes(t *testing.T) {
	if _, ok := decodeConditionalReplay([]byte(`{"outcome":"rejected_stale","resulting_etag":"\"7:4\""}`), `"7:4"`); ok {
		t.Fatal("rejected replay accepted on 200")
	}
	if _, ok := decodeConditionalReplay([]byte(`{"outcome":"applied_changed","resulting_etag":"W/\"7:4\""}`), ""); ok {
		t.Fatal("weak resulting ETag accepted")
	}
	res, ok := decodeConditionalReplay([]byte(`{"outcome":"applied_noop","resulting_etag":"\"7:4\""}`), `"7:4"`)
	if !ok || res.ETag != `"7:4"` || res.Settings != (PoolSettings{}) {
		t.Fatalf("valid noop replay rejected: %+v", res)
	}
}

func TestConditionalSettingsConcurrentAccess(t *testing.T) {
	c, f := conditionalServer(t, nil)
	ref := EntityRef{Kind: EntityKindRepository, ID: managedRepoID}
	const workers = 8
	const iterations = 5
	failures := make([]error, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				got, err := c.GetConditionalSettings(context.Background(), ref, managedPoolID)
				if err != nil {
					failures[w] = err
					return
				}
				priority := uint64(w*iterations + i + 1)
				res, err := c.PatchConditionalSettings(context.Background(), ref, managedPoolID, got.ETag, fmt.Sprintf("worker-%d-%d", w, i), PoolSettingsPatch{Priority: value(priority)})
				if err != nil {
					failures[w] = err
					return
				}
				if res.Settings.Priority != priority || !conditionalETag(res.ETag) {
					failures[w] = ErrManagement
					return
				}
			}
		}(w)
	}
	wg.Wait()
	for w, err := range failures {
		if err != nil {
			t.Fatalf("worker %d: %v", w, err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gets != workers*iterations || f.patches != workers*iterations {
		t.Fatalf("accounting gets=%d patches=%d", f.gets, f.patches)
	}
	for i, match := range f.ifMatches {
		if !conditionalETag(match) {
			t.Fatalf("patch %d dispatched without a strong ETag", i)
		}
	}
	for i, key := range f.idempotency {
		if key == "" || !strings.HasPrefix(key, "worker-") {
			t.Fatalf("patch %d missing Idempotency-Key", i)
		}
	}
}
