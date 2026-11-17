// Copyright 2025 Cloudbase Solutions SRL
//
//	Licensed under the Apache License, Version 2.0 (the "License"); you may
//	not use this file except in compliance with the License. You may obtain
//	a copy of the License at
//
//	     http://www.apache.org/licenses/LICENSE-2.0
//
//	Unless required by applicable law or agreed to in writing, software
//	distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
//	WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
//	License for the specific language governing permissions and limitations
//	under the License.
package cmd

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/cloudbase/garm/cmd/garm-cli/config"
)

// newTestRawClient points the raw HTTP client at a mock server. No live
// Garm server is ever contacted; all tests use httptest.
func newTestRawClient(srv *httptest.Server) *rawClient {
	return &rawClient{
		BaseURL:    srv.URL + "/api/v1",
		Token:      "test-token",
		HTTPClient: srv.Client(),
	}
}

// Isolate the pending-request journal from the real user configuration
// directory for every test in this package.
func init() {
	dir, err := os.MkdirTemp("", "garm-cli-exp-pending")
	if err == nil {
		experimentalPendingPathOverride = filepath.Join(dir, "pending.json")
	}
}

// resetExpFlags clears the package-level flag backing variables so state
// cannot bleed between executions of the experimental command tree.
func resetExpFlags() {
	expPoolEntityRepo = ""
	expPoolEntityOrg = ""
	expPoolEntityEnterprise = ""
	expPoolID = ""
	expPoolSettingsETag = ""
	expPoolSettingsDraftFile = ""
	expPoolSettingsRequestID = ""
	expPoolSettingsResolve = false
	expPoolSettingsPendingClearConfirm = false
	expPriority = 0
	expMaxRunners = 0
	expMinIdleRunners = 0
}

func executeExperimental(cli *rawClient, args ...string) (*cobra.Command, error) {
	resetExpFlags()
	root := &cobra.Command{Use: "garm-cli", SilenceErrors: true, SilenceUsage: true}
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.AddCommand(newExperimentalCmd(cli))
	root.SetArgs(append([]string{"experimental", "pool-settings"}, args...))
	return root, root.Execute()
}

// roundTripperFunc adapts a function into an http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPoolSettingsGetCarriesAuthAndPath(t *testing.T) {
	clearExperimentalPending()
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("ETag", `"100:7"`)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"priority":100,"max_runners":10,"min_idle_runners":1}`)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "get", "--repo", "repo-1", "--pool", "pool-1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("expected bearer token on GET, got %q", gotAuth)
	}
	wantPath := "/api/v1/offline/entities/repository/repo-1/pools/pool-1/settings"
	if gotPath != wantPath {
		t.Errorf("expected path %q, got %q", wantPath, gotPath)
	}
}

func TestPoolSettingsPatchApplied(t *testing.T) {
	clearExperimentalPending()
	var gotIfMatch, gotContentType string
	var gotBody map[string]uint
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("ETag", `"100:7"`)
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"priority":100,"max_runners":10,"min_idle_runners":1}`)
		case http.MethodPatch:
			gotIfMatch = r.Header.Get("If-Match")
			gotContentType = r.Header.Get("Content-Type")
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Errorf("failed to decode patch body: %v", err)
			}
			w.Header().Set("ETag", `"100:8"`)
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"priority":100,"max_runners":20,"min_idle_runners":1}`)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "patch", "--repo", "repo-1", "--pool", "pool-1",
		"--etag", `"100:7"`, "--max-runners", "20")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if gotIfMatch != `"100:7"` {
		t.Errorf("expected If-Match %q, got %q", `"100:7"`, gotIfMatch)
	}
	if gotContentType != "application/json" {
		t.Errorf("expected application/json content type, got %q", gotContentType)
	}
	if len(gotBody) != 1 || gotBody["max_runners"] != 20 {
		t.Errorf("expected strict body {max_runners:20}, got %v", gotBody)
	}
}

func TestPoolSettingsPatchConflictPreservesDraftAndProvesNonEffect(t *testing.T) {
	clearExperimentalPending()
	var patchCalls atomic.Int32
	var appliedPatches []map[string]uint
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("ETag", `"100:9"`)
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"priority":100,"max_runners":10,"min_idle_runners":1}`)
		case http.MethodPatch:
			patchCalls.Add(1)
			var body map[string]uint
			_ = json.NewDecoder(r.Body).Decode(&body)
			appliedPatches = append(appliedPatches, body)
			// Stale precondition: reject and carry the current ETag.
			w.Header().Set("ETag", `"100:9"`)
			w.WriteHeader(http.StatusPreconditionFailed)
			fmt.Fprint(w, `{"error":"stale"}`)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	draft := `{"priority":50,"max_runners":5,"min_idle_runners":2}`
	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "patch", "--org", "org-1", "--pool", "pool-1",
		"--etag", `"100:1"`, "--file", writeTempDraft(t, draft))
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "conflict") {
		t.Errorf("expected conflict classification, got: %v", err)
	}
	if !strings.Contains(msg, `"100:9"`) {
		t.Errorf("expected fresh ETag from 412 response to be surfaced, got: %v", err)
	}
	if !strings.Contains(msg, draft) {
		t.Errorf("expected operator draft to be preserved in the error output, got: %v", err)
	}
	if patchCalls.Load() != 1 {
		t.Errorf("expected exactly one PATCH dispatch (no retry), got %d", patchCalls.Load())
	}
	// 412 proves non-effect: nothing was applied server-side.
	if len(appliedPatches) != 1 {
		t.Errorf("server recorded %d patch attempts", len(appliedPatches))
	}
}

func TestPoolSettingsPatchIDReuseIsRejectedNotUnknown(t *testing.T) {
	clearExperimentalPending()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("ETag", `"100:9"`)
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"error":"Idempotency-Key \"k\" was already used with a different payload or pool"}`)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "patch", "--repo", "repo-1", "--pool", "pool-1",
		"--etag", `"100:8"`, "--priority", "7")
	if err == nil {
		t.Fatal("expected rejection, got nil")
	}
	var pse *poolSettingsError
	if !errors.As(err, &pse) {
		t.Fatalf("expected classified error, got: %v", err)
	}
	if pse.outcome != outcomeRejected {
		t.Errorf("409 must classify as rejected, got %v", pse.outcome)
	}
	if pse.outcome == outcomeUnknown || strings.Contains(err.Error(), unknownHoldMessage) {
		t.Errorf("definitive 409 refusal must not be held as UNKNOWN: %v", err)
	}
}

func TestPoolSettingsPatchReplayEnvelopeReportsStoredOutcome(t *testing.T) {
	clearExperimentalPending()
	var patchCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		patchCalls.Add(1)
		// Answered from a durable request receipt: the settings tuple is
		// not stored server-side, only the outcome and resulting ETag.
		w.Header().Set("ETag", `"100:9"`)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"outcome":"applied_changed","resulting_etag":"\"100:9\""}`)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "patch", "--repo", "repo-1", "--pool", "pool-1",
		"--etag", `"100:8"`, "--priority", "7")
	if err != nil {
		t.Fatalf("replay of a known-applied request must not be an error: %v", err)
	}
	if patchCalls.Load() != 1 {
		t.Errorf("expected exactly one PATCH dispatch, got %d", patchCalls.Load())
	}
}

func TestPoolSettingsPatchUnknownHoldOnLostResponse(t *testing.T) {
	clearExperimentalPending()
	var dispatches atomic.Int32
	cli := &rawClient{
		BaseURL: "http://offline.invalid/api/v1",
		Token:   "test-token",
		HTTPClient: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			dispatches.Add(1)
			// Response lost after dispatch.
			return nil, errors.New("connection reset by peer")
		})},
	}

	_, err := executeExperimental(cli, "patch", "--repo", "repo-1", "--pool", "pool-1",
		"--etag", `"100:7"`, "--max-runners", "20")
	if err == nil {
		t.Fatal("expected UNKNOWN hold error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "UNKNOWN") || !strings.Contains(msg, "Do NOT auto-retry") {
		t.Errorf("expected UNKNOWN hold message, got: %v", err)
	}
	if !strings.Contains(msg, `"max_runners":20`) {
		t.Errorf("expected held draft to be surfaced, got: %v", err)
	}
	if dispatches.Load() != 1 {
		t.Errorf("expected exactly one dispatch (no retry), got %d", dispatches.Load())
	}
}

func TestPoolSettingsPatchUnknownHoldOnAmbiguousStatus(t *testing.T) {
	clearExperimentalPending()
	var patchCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		patchCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":"try later"}`)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "patch", "--enterprise", "ent-1", "--pool", "pool-1",
		"--etag", `"100:7"`, "--priority", "10")
	if err == nil {
		t.Fatal("expected UNKNOWN hold error for 503, got nil")
	}
	if !strings.Contains(err.Error(), "UNKNOWN") {
		t.Errorf("expected UNKNOWN hold message for ambiguous 503, got: %v", err)
	}
	if patchCalls.Load() != 1 {
		t.Errorf("expected no retry after ambiguous status, got %d dispatches", patchCalls.Load())
	}
}

func TestPoolSettingsPatchRejectedWithoutETagSendsNothing(t *testing.T) {
	clearExperimentalPending()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "patch", "--repo", "repo-1", "--pool", "pool-1", "--priority", "10")
	if err == nil {
		t.Fatal("expected refusal to patch without --etag, got nil")
	}
	if !strings.Contains(err.Error(), "refusing to patch without --etag") {
		t.Errorf("expected unconditional-write refusal, got: %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("expected zero HTTP dispatches when no ETag is given, got %d", calls.Load())
	}
}

func TestPoolSettingsGetRejectedIsNotUnknown(t *testing.T) {
	clearExperimentalPending()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":"not allowed"}`)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "get", "--repo", "repo-1", "--pool", "pool-1")
	if err == nil {
		t.Fatal("expected rejected error, got nil")
	}
	if strings.Contains(err.Error(), "UNKNOWN") {
		t.Errorf("a definitive 403 must not be reported as UNKNOWN: %v", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("expected status to be surfaced, got: %v", err)
	}
}

func TestPoolSettingsPatchSendsIdempotencyKey(t *testing.T) {
	clearExperimentalPending()
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		gotKey = r.Header.Get("Idempotency-Key")
		w.Header().Set("ETag", `"100:9"`)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"priority":7,"max_runners":10,"min_idle_runners":1}`)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "patch", "--repo", "repo-1", "--pool", "pool-1",
		"--etag", `"100:8"`, "--priority", "7")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if gotKey == "" {
		t.Fatal("expected an Idempotency-Key header on the PATCH")
	}

	// Operator-supplied request id is sent verbatim.
	gotKey = ""
	_, err = executeExperimental(cli, "patch", "--repo", "repo-1", "--pool", "pool-1",
		"--etag", `"100:8"`, "--priority", "7", "--request-id", "my-request-1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if gotKey != "my-request-1" {
		t.Fatalf("expected --request-id to be sent as Idempotency-Key, got %q", gotKey)
	}
}

func TestPoolSettingsPatchAppliedClearsPendingJournal(t *testing.T) {
	clearExperimentalPending()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("ETag", `"100:9"`)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"priority":7,"max_runners":10,"min_idle_runners":1}`)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "patch", "--repo", "repo-1", "--pool", "pool-1",
		"--etag", `"100:8"`, "--priority", "7")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if _, statErr := os.Stat(experimentalPendingPath()); !os.IsNotExist(statErr) {
		t.Fatalf("applied outcome must clear the pending journal, stat err: %v", statErr)
	}
}

func TestPoolSettingsPatchDefinitiveRejectionClearsPendingJournal(t *testing.T) {
	clearExperimentalPending()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("ETag", `"100:9"`)
		w.WriteHeader(http.StatusPreconditionFailed)
		fmt.Fprint(w, `{"error":"stale"}`)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "patch", "--repo", "repo-1", "--pool", "pool-1",
		"--etag", `"100:8"`, "--priority", "7")
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if _, statErr := os.Stat(experimentalPendingPath()); !os.IsNotExist(statErr) {
		t.Fatalf("definitive outcome must clear the pending journal, stat err: %v", statErr)
	}
}

func TestPoolSettingsPatchUnknownKeepsPendingJournal(t *testing.T) {
	clearExperimentalPending()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// Committed-but-unreadable response: 200 with a body that is not a
		// settings document and not a replay outcome.
		w.Header().Set("ETag", `"100:9"`)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"unexpected":true}`)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "patch", "--repo", "repo-1", "--pool", "pool-1",
		"--etag", `"100:8"`, "--priority", "7")
	if err == nil {
		t.Fatal("expected UNKNOWN hold on unreadable applied response")
	}
	var pse *poolSettingsError
	if !errors.As(err, &pse) || pse.outcome != outcomeUnknown {
		t.Fatalf("expected UNKNOWN outcome, got: %v", err)
	}
	data, readErr := os.ReadFile(experimentalPendingPath())
	if readErr != nil {
		t.Fatalf("UNKNOWN outcome must retain the pending journal: %v", readErr)
	}
	var rec experimentalPendingRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("pending journal is not readable: %v", err)
	}
	if rec.RequestID == "" || rec.Payload == "" || rec.ETag != `"100:8"` {
		t.Fatalf("pending journal does not record the exact request: %+v", rec)
	}
}

func TestExperimentalRegistrationIsOptIn(t *testing.T) {
	t.Setenv(experimentalEnvVar, "")
	root := &cobra.Command{Use: "garm-cli"}
	registerExperimental(root)
	for _, c := range root.Commands() {
		if c.Name() == "experimental" {
			t.Fatal("experimental subcommand must be absent when the opt-in variable is unset")
		}
	}

	t.Setenv(experimentalEnvVar, "1")
	root = &cobra.Command{Use: "garm-cli"}
	registerExperimental(root)
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "experimental" {
			found = true
			if !c.Hidden {
				t.Error("experimental command group should be hidden")
			}
		}
	}
	if !found {
		t.Fatal("experimental subcommand must be present when the opt-in variable is set")
	}
}

func TestParsePoolSettingsDraftStrict(t *testing.T) {
	if _, err := parsePoolSettingsDraft([]byte(`{"priority":1,"bogus":2}`)); err == nil {
		t.Error("expected unsupported field to be rejected")
	}
	if _, err := parsePoolSettingsDraft([]byte(`{}`)); err == nil {
		t.Error("expected empty draft to be rejected (at least one field required)")
	}
	patch, err := parsePoolSettingsDraft([]byte(`{"priority":1,"max_runners":4}`))
	if err != nil {
		t.Fatalf("expected valid draft, got: %v", err)
	}
	if patch.Priority == nil || *patch.Priority != 1 || patch.MaxRunners == nil || *patch.MaxRunners != 4 {
		t.Errorf("unexpected parsed patch: %+v", patch)
	}
	if patch.MinIdleRunners != nil {
		t.Errorf("min_idle_runners should be unset, got %v", *patch.MinIdleRunners)
	}
}

// seedPendingRecord writes a pending journal record through the production
// path so tests exercise the same serialization the CLI relies on.
func seedPendingRecord(t *testing.T, rec experimentalPendingRecord) {
	t.Helper()
	clearExperimentalPending()
	if err := writeExperimentalPending(rec); err != nil {
		t.Fatalf("seed pending journal: %v", err)
	}
}

func TestPoolSettingsPatchRefusesWhenPendingExists(t *testing.T) {
	withProfile(t, "profile-a")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	rec := experimentalPendingRecord{
		RequestID:  "rec-1",
		EntityType: "repository",
		EntityID:   "repo-1",
		PoolID:     "pool-1",
		ETag:       `"100:8"`,
		Payload:    `{"priority":7}`,
		At:         "2026-11-16T10:00:00Z",
	}
	rec.Profile, rec.Server = experimentalCurrentBinding(cli)
	seedPendingRecord(t, rec)

	_, err := executeExperimental(cli, "patch", "--repo", "repo-1", "--pool", "pool-1",
		"--etag", `"100:9"`, "--priority", "9")
	if err == nil {
		t.Fatal("expected refusal to send a new write while a request is pending")
	}
	msg := err.Error()
	for _, want := range []string{"unresolved request", "rec-1", "repository repo-1", "pool-1", "2026-11-16T10:00:00Z", "--resolve"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected refusal to mention %q, got: %v", want, err)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("expected zero HTTP requests when a pending journal exists, got %d", calls.Load())
	}

	// The refusal must not overwrite the pending journal.
	data, readErr := os.ReadFile(experimentalPendingPath())
	if readErr != nil {
		t.Fatalf("pending journal must survive the refusal: %v", readErr)
	}
	var kept experimentalPendingRecord
	if err := json.Unmarshal(data, &kept); err != nil {
		t.Fatalf("pending journal is no longer readable: %v", err)
	}
	if kept.RequestID != "rec-1" || kept.Payload != `{"priority":7}` || kept.ETag != `"100:8"` {
		t.Fatalf("pending journal was overwritten by the refused invocation: %+v", kept)
	}
}

func TestPoolSettingsPatchResolveResendsJournaledRequest(t *testing.T) {
	withProfile(t, "profile-a")
	var calls atomic.Int32
	var gotKey, gotIfMatch, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotKey = r.Header.Get("Idempotency-Key")
		gotIfMatch = r.Header.Get("If-Match")
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("ETag", `"100:9"`)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"priority":7,"max_runners":10,"min_idle_runners":1}`)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	rec := experimentalPendingRecord{
		RequestID:  "rec-1",
		EntityType: "repository",
		EntityID:   "repo-1",
		PoolID:     "pool-1",
		ETag:       `"100:8"`,
		Payload:    `{"priority":7}`,
		At:         "2026-11-16T10:00:00Z",
	}
	rec.Profile, rec.Server = experimentalCurrentBinding(cli)
	seedPendingRecord(t, rec)
	// Deliberately no entity/pool/payload flags: the journal is authoritative.
	_, err := executeExperimental(cli, "patch", "--resolve")
	if err != nil {
		t.Fatalf("expected --resolve to succeed, got: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected exactly one PATCH, got %d", calls.Load())
	}
	if gotKey != "rec-1" {
		t.Errorf("expected Idempotency-Key %q, got %q", "rec-1", gotKey)
	}
	if gotIfMatch != `"100:8"` {
		t.Errorf("expected If-Match %q, got %q", `"100:8"`, gotIfMatch)
	}
	if gotBody != `{"priority":7}` {
		t.Errorf("expected the exact journaled payload, got %q", gotBody)
	}
	wantPath := "/api/v1/offline/entities/repository/repo-1/pools/pool-1/settings"
	if gotPath != wantPath {
		t.Errorf("expected resolution against %q, got %q", wantPath, gotPath)
	}
	if _, statErr := os.Stat(experimentalPendingPath()); !os.IsNotExist(statErr) {
		t.Fatalf("definitive outcome must clear the pending journal, stat err: %v", statErr)
	}
}

func TestPoolSettingsPatchResolveWithoutPending(t *testing.T) {
	clearExperimentalPending()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "patch", "--resolve")
	if err == nil {
		t.Fatal("expected an error when there is nothing to resolve")
	}
	if !strings.Contains(err.Error(), "nothing to resolve") {
		t.Errorf("expected a clear no-pending error, got: %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("expected zero HTTP requests, got %d", calls.Load())
	}
}

func TestPoolSettingsPatchResolveRejectsAmbiguousFlags(t *testing.T) {
	clearExperimentalPending()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "patch", "--resolve", "--priority", "7")
	if err == nil {
		t.Fatal("expected an error for ambiguous --resolve usage")
	}
	if !strings.Contains(err.Error(), "--resolve") || !strings.Contains(err.Error(), "--priority") {
		t.Errorf("expected the error to explain the --resolve/--priority ambiguity, got: %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("expected zero HTTP requests, got %d", calls.Load())
	}
}

func TestPoolSettingsPatchJournaledAbort(t *testing.T) {
	clearExperimentalPending()

	// Point the journal at a path that names an existing directory: staging
	// succeeds, but the temp file cannot be renamed over the directory, so
	// journaling fails and the write must abort unjournaled.
	blocked := filepath.Join(t.TempDir(), "pending.json")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatalf("setup blocked journal path: %v", err)
	}
	oldOverride := experimentalPendingPathOverride
	experimentalPendingPathOverride = blocked
	t.Cleanup(func() { experimentalPendingPathOverride = oldOverride })

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "patch", "--repo", "repo-1", "--pool", "pool-1",
		"--etag", `"100:8"`, "--priority", "7")
	if err == nil {
		t.Fatal("expected the patch to abort when the journal cannot be written")
	}
	msg := err.Error()
	if !strings.Contains(msg, "journal") || !strings.Contains(msg, "unjournaled") {
		t.Errorf("expected a journaling failure abort, got: %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("expected zero HTTP requests when journaling fails, got %d", calls.Load())
	}
}

// TestPoolSettingsPatchDirsyncFailureAborts: a parent-directory fsync failure
// after the atomic rename is exactly as fatal as a journal-write failure —
// the write aborts before dispatch, fail closed. The renamed record stays on
// disk, undispatched, so a later --resolve settles it deliberately.
func TestPoolSettingsPatchDirsyncFailureAborts(t *testing.T) {
	withProfile(t, "profile-dirsync")
	clearExperimentalPending()
	t.Cleanup(func() { clearExperimentalPending() })
	oldSync := experimentalSyncDir
	experimentalSyncDir = func(string) error { return errors.New("dirsync boom") }
	t.Cleanup(func() { experimentalSyncDir = oldSync })

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "patch", "--repo", "repo-1", "--pool", "pool-1",
		"--etag", `"100:8"`, "--priority", "7")
	if err == nil {
		t.Fatal("expected a dirsync failure to abort the write before dispatch")
	}
	msg := err.Error()
	if !strings.Contains(msg, "fsync") || !strings.Contains(msg, "pending-request journal") {
		t.Errorf("expected a dirsync journaling failure, got: %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("expected zero HTTP dispatches when the journal cannot be made durable, got %d", calls.Load())
	}
	data, readErr := os.ReadFile(experimentalPendingPath())
	if readErr != nil {
		t.Fatalf("the undispatched record must be kept for a deliberate --resolve: %v", readErr)
	}
	var rec experimentalPendingRecord
	if err := json.Unmarshal(data, &rec); err != nil || rec.Payload != `{"priority":7}` {
		t.Fatalf("the kept record must hold the exact undispatched request: %+v (err %v)", rec, err)
	}
}

// TestPoolSettingsPatchClearUnlinkFailureIsLoud: when the journal cannot be
// unlinked after a definitive outcome, the failure must be surfaced loudly
// and the record kept — never silently continued — because a later --resolve
// re-asks the server and the durable receipt makes it idempotent.
func TestPoolSettingsPatchClearUnlinkFailureIsLoud(t *testing.T) {
	withProfile(t, "profile-unlink")
	clearExperimentalPending()
	t.Cleanup(func() { clearExperimentalPending() })
	oldRemove := experimentalRemovePending
	experimentalRemovePending = func(string) error { return errors.New("unlink boom") }
	t.Cleanup(func() { experimentalRemovePending = oldRemove })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("ETag", `"100:9"`)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"priority":7,"max_runners":10,"min_idle_runners":1}`)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "patch", "--repo", "repo-1", "--pool", "pool-1",
		"--etag", `"100:8"`, "--priority", "7")
	if err == nil {
		t.Fatal("an unlink failure after an applied outcome must be surfaced loudly, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"could not be cleared", "kept", "--resolve"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected the loud failure to mention %q, got: %v", want, err)
		}
	}
	if _, statErr := os.Stat(experimentalPendingPath()); statErr != nil {
		t.Fatalf("the record must be kept when the unlink fails: %v", statErr)
	}
}

// TestPoolSettingsPatchReplayEnvelopeETagMismatchKeepsJournal: a 200 receipt
// replay whose resulting_etag disagrees with the response's own ETag header
// is untrustworthy (mirroring the web client). The outcome stays UNKNOWN and
// only a fully validated applied replay may clear the journal.
func TestPoolSettingsPatchReplayEnvelopeETagMismatchKeepsJournal(t *testing.T) {
	clearExperimentalPending()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("ETag", `"100:9"`)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"outcome":"applied_changed","resulting_etag":"\"100:10\""}`)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "patch", "--repo", "repo-1", "--pool", "pool-1",
		"--etag", `"100:8"`, "--priority", "7")
	if err == nil {
		t.Fatal("expected UNKNOWN hold on a replay envelope that disagrees with the response ETag")
	}
	var pse *poolSettingsError
	if !errors.As(err, &pse) || pse.outcome != outcomeUnknown {
		t.Fatalf("expected UNKNOWN outcome, got: %v", err)
	}
	if _, statErr := os.Stat(experimentalPendingPath()); statErr != nil {
		t.Fatalf("a rejected replay must keep the pending journal: %v", statErr)
	}
}

// TestPoolSettingsPatchResolvePreconditionFailedKeepsJournal: a 412 while
// resolving is NOT proof of non-effect. After a server-side loss of the
// durable request-receipt table (restore/reinstall), the original write may
// have committed even though the server can no longer recognise the request
// id, so the outcome stays UNKNOWN, the journal is kept and recovery
// guidance points at the gated manual clear — the same reasoning as the web
// client fix.
func TestPoolSettingsPatchResolvePreconditionFailedKeepsJournal(t *testing.T) {
	withProfile(t, "profile-a")
	var patchCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		patchCalls.Add(1)
		w.Header().Set("ETag", `"100:12"`)
		w.WriteHeader(http.StatusPreconditionFailed)
		fmt.Fprint(w, `{"error":"stale"}`)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	seedPendingRecord(t, boundRecord(cli, "rec-412"))

	_, err := executeExperimental(cli, "patch", "--resolve")
	if err == nil {
		t.Fatal("expected a 412 on resolve to hold UNKNOWN, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"UNKNOWN", "receipt", "pending clear --confirmed-ineffective", "rec-412"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected the hold to mention %q, got: %v", want, err)
		}
	}
	var pse *poolSettingsError
	if !errors.As(err, &pse) || pse.outcome != outcomeUnknown {
		t.Fatalf("a 412 on resolve must be held as UNKNOWN, got: %v", err)
	}
	if patchCalls.Load() != 1 {
		t.Errorf("expected exactly one resolve dispatch (no retry), got %d", patchCalls.Load())
	}
	data, readErr := os.ReadFile(experimentalPendingPath())
	if readErr != nil {
		t.Fatalf("the journal must survive a 412 on resolve: %v", readErr)
	}
	var kept experimentalPendingRecord
	if err := json.Unmarshal(data, &kept); err != nil || kept.RequestID != "rec-412" {
		t.Fatalf("the journal must keep the unresolved request, got %+v (err %v)", kept, err)
	}
}

// TestPoolSettingsPatchLostResponseSocketLevel is the socket-level variant of
// the lost-response UNKNOWN hold: the server hijacks the connection, reads
// the full request and closes without responding. The CLI must hold UNKNOWN
// with the journal intact, and a later --resolve must be answered by the
// server from the durable request receipt.
func TestPoolSettingsPatchLostResponseSocketLevel(t *testing.T) {
	withProfile(t, "profile-socket")
	clearJournalAndLock(t)

	var requests atomic.Int32
	var gotKey atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if n == 1 {
			// Read the request fully, then drop the connection without any
			// response bytes: the write was dispatched, the answer lost.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("httptest response writer is not a hijacker")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Errorf("hijack failed: %v", err)
				return
			}
			_, _ = io.ReadAll(r.Body)
			_ = conn.Close()
			return
		}
		gotKey.Store(r.Header.Get("Idempotency-Key"))
		w.Header().Set("ETag", `"100:9"`)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"priority":7,"max_runners":10,"min_idle_runners":1}`)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "patch", "--repo", "repo-1", "--pool", "pool-1",
		"--etag", `"100:8"`, "--priority", "7", "--request-id", "socket-req-1")
	if err == nil {
		t.Fatal("expected UNKNOWN hold when the response is lost at the socket level")
	}
	msg := err.Error()
	if !strings.Contains(msg, "UNKNOWN") || !strings.Contains(msg, "Do NOT auto-retry") {
		t.Errorf("expected UNKNOWN hold message, got: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("expected exactly one dispatched request, got %d", requests.Load())
	}
	data, readErr := os.ReadFile(experimentalPendingPath())
	if readErr != nil {
		t.Fatalf("UNKNOWN outcome must retain the pending journal: %v", readErr)
	}
	var rec experimentalPendingRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("pending journal is not readable: %v", err)
	}
	if rec.RequestID != "socket-req-1" || rec.Payload != `{"priority":7}` || rec.ETag != `"100:8"` {
		t.Fatalf("pending journal does not record the exact dispatched request: %+v", rec)
	}

	// The server is reachable again: --resolve re-presents the exact
	// journaled request and the server answers.
	_, err = executeExperimental(cli, "patch", "--resolve")
	if err != nil {
		t.Fatalf("resolve after a lost response must succeed: %v", err)
	}
	if got := gotKey.Load(); got != "socket-req-1" {
		t.Errorf("expected the server to see the journaled request id, got %v", got)
	}
	if _, statErr := os.Stat(experimentalPendingPath()); !os.IsNotExist(statErr) {
		t.Fatalf("resolve must clear the journal, stat err: %v", statErr)
	}
}

// TestPoolSettingsPendingRecordValidationFailsClosed: strict shape validation
// on load — a record with an empty binding field can never have been written
// by this version, so the journal is corrupt, must fail closed with the
// manual-recovery message and must never dispatch.
func TestPoolSettingsPendingRecordValidationFailsClosed(t *testing.T) {
	withProfile(t, "profile-a")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cli := newTestRawClient(srv)

	rec := boundRecord(cli, "rec-invalid-1")
	rec.Profile = ""
	seedPendingRecord(t, rec)

	_, err := executeExperimental(cli, "patch", "--resolve")
	if err == nil {
		t.Fatal("expected a corrupt record to fail closed on load")
	}
	msg := err.Error()
	for _, want := range []string{"profile binding is missing", "kept", "--resolve", "pending clear --confirmed-ineffective"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected the failure to mention %q, got: %v", want, err)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("a corrupt journal must refuse before any dispatch, got %d calls", calls.Load())
	}
}

func TestPoolSettingsPendingRecordUnreadableFailsClosed(t *testing.T) {
	clearJournalAndLock(t)
	if err := os.WriteFile(experimentalPendingPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("could not plant an unreadable journal: %v", err)
	}
	_, _, err := loadExperimentalPending()
	if err == nil {
		t.Fatal("an unreadable journal must fail closed")
	}
	msg := err.Error()
	if !strings.Contains(msg, "kept") || !strings.Contains(msg, "pending clear --confirmed-ineffective") {
		t.Errorf("expected the manual-recovery message, got: %v", err)
	}
}

func TestPoolSettingsPendingClearRequiresFlag(t *testing.T) {
	withProfile(t, "profile-a")
	var getCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		getCalls.Add(1)
		w.Header().Set("ETag", `"100:9"`)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"priority":100,"max_runners":10,"min_idle_runners":1}`)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	seedPendingRecord(t, boundRecord(cli, "rec-clear-1"))

	_, err := executeExperimental(cli, "pending", "clear")
	if err == nil {
		t.Fatal("expected refusal to clear without --confirmed-ineffective")
	}
	msg := err.Error()
	for _, want := range []string{"refusing", "--confirmed-ineffective"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected refusal to mention %q, got: %v", want, err)
		}
	}
	if getCalls.Load() != 0 {
		t.Errorf("the refusal must happen before any read, got %d GETs", getCalls.Load())
	}
	if _, statErr := os.Stat(experimentalPendingPath()); statErr != nil {
		t.Fatalf("journal must be kept after the refusal: %v", statErr)
	}
}

func TestPoolSettingsPendingClearRefusesWhenGetFails(t *testing.T) {
	withProfile(t, "profile-a")
	var getCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		getCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"boom"}`)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	seedPendingRecord(t, boundRecord(cli, "rec-clear-2"))

	_, err := executeExperimental(cli, "pending", "clear", "--confirmed-ineffective")
	if err == nil {
		t.Fatal("expected refusal to clear when the fresh read fails")
	}
	msg := err.Error()
	for _, want := range []string{"refusing to clear", "kept"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected refusal to mention %q, got: %v", want, err)
		}
	}
	if getCalls.Load() != 1 {
		t.Errorf("expected exactly one GET before the refusal, got %d", getCalls.Load())
	}
	if _, statErr := os.Stat(experimentalPendingPath()); statErr != nil {
		t.Fatalf("journal must be kept when the fresh read fails: %v", statErr)
	}
}

func TestPoolSettingsPendingClearClearsAfterFreshRead(t *testing.T) {
	withProfile(t, "profile-a")
	var getCalls atomic.Int32
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		getCalls.Add(1)
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		gotPath = r.URL.Path
		w.Header().Set("ETag", `"100:11"`)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"priority":100,"max_runners":10,"min_idle_runners":1}`)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	seedPendingRecord(t, boundRecord(cli, "rec-clear-3"))

	var execErr error
	stderr := captureStderr(t, func() {
		_, execErr = executeExperimental(cli, "pending", "clear", "--confirmed-ineffective")
	})
	if execErr != nil {
		t.Fatalf("expected the clear to succeed after a fresh read, got: %v", execErr)
	}
	for _, want := range []string{"WARNING", "MANUALLY", "rec-clear-3", `"100:11"`} {
		if !strings.Contains(stderr, want) {
			t.Errorf("expected the warning to mention %q, got: %s", want, stderr)
		}
	}
	if getCalls.Load() != 1 {
		t.Errorf("expected exactly one GET, got %d", getCalls.Load())
	}
	wantPath := "/api/v1/offline/entities/repository/repo-1/pools/pool-1/settings"
	if gotPath != wantPath {
		t.Errorf("expected the fresh read against %q, got %q", wantPath, gotPath)
	}
	if _, statErr := os.Stat(experimentalPendingPath()); !os.IsNotExist(statErr) {
		t.Fatalf("journal must be cleared after a confirmed ineffective request, stat err: %v", statErr)
	}
}

func TestPoolSettingsPendingClearNothingPending(t *testing.T) {
	clearJournalAndLock(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cli := newTestRawClient(srv)
	_, err := executeExperimental(cli, "pending", "clear", "--confirmed-ineffective")
	if err == nil {
		t.Fatal("expected an error when there is nothing to clear")
	}
	if !strings.Contains(err.Error(), "nothing to clear") {
		t.Errorf("expected a clear no-pending error, got: %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("expected zero HTTP requests, got %d", calls.Load())
	}
}

func writeTempDraft(t *testing.T, content string) string {
	t.Helper()
	f := t.TempDir() + "/draft.json"
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write draft file: %v", err)
	}
	return f
}

// withProfile switches the package-level effective manager for the duration
// of a test so binding checks see a concrete active profile name.
func withProfile(t *testing.T, name string) {
	t.Helper()
	old := mgr
	mgr = config.Manager{Name: name}
	t.Cleanup(func() { mgr = old })
}

// clearJournalAndLock resets both the pending journal and its advisory lock
// so lock tests are independent of any state a previous test left behind.
func clearJournalAndLock(t *testing.T) {
	t.Helper()
	clearExperimentalPending()
	if lockPath := experimentalJournalLockPath(); lockPath != "" {
		_ = os.Remove(lockPath)
	}
}

// captureStderr swaps os.Stderr for a pipe while fn runs and returns what was
// written, so tests can assert loud warnings.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("could not create stderr pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	fn()
	_ = w.Close()
	os.Stderr = old
	return <-done
}

// boundRecord builds a fully valid journaled record bound to the current
// invocation context (active profile + effective server of cli).
func boundRecord(cli *rawClient, requestID string) experimentalPendingRecord {
	rec := experimentalPendingRecord{
		RequestID:  requestID,
		EntityType: "repository",
		EntityID:   "repo-1",
		PoolID:     "pool-1",
		ETag:       `"100:8"`,
		Payload:    `{"priority":7}`,
		At:         "2026-11-16T10:00:00Z",
	}
	rec.Profile, rec.Server = experimentalCurrentBinding(cli)
	return rec
}

func TestExperimentalJournalLockFailsClosedWhileHeld(t *testing.T) {
	clearJournalAndLock(t)
	release, err := acquireExperimentalJournalLock()
	if err != nil {
		t.Fatalf("first acquisition failed: %v", err)
	}

	_, err = acquireExperimentalJournalLock()
	if err == nil {
		t.Fatal("second acquisition must fail closed while a live process holds the lock")
	}
	msg := err.Error()
	for _, want := range []string{"locked", "process", strconv.Itoa(os.Getpid())} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected refusal to mention %q, got: %v", want, err)
		}
	}

	// The lock file is persistent: releasing closes the kernel lock but
	// never unlinks the file, and re-acquisition must succeed immediately.
	release()
	lockPath := experimentalJournalLockPath()
	if _, statErr := os.Stat(lockPath); statErr != nil {
		t.Fatalf("the persistent lock file must survive release: %v", statErr)
	}
	release2, err := acquireExperimentalJournalLock()
	if err != nil {
		t.Fatalf("re-acquisition right after release failed: %v", err)
	}
	release2()
}

// TestExperimentalJournalLockSurvivesKill9 is the stale-lock regression: a
// holder killed with kill -9 releases the kernel advisory lock with its
// dying fd, so the survivor acquires immediately — the old pid-probe and
// lock-file deletion dance (which could delete a lock a third process had
// legitimately acquired) no longer exists.
func TestExperimentalJournalLockSurvivesKill9(t *testing.T) {
	if os.Getenv("GARM_CLI_EXP_LOCK_KILL_CHILD") == "1" {
		experimentalJournalLockHoldChild()
		return
	}
	if runtime.GOOS != "linux" {
		t.Skip("kill -9 lock-release regression test requires the POSIX signal model")
	}
	clearJournalAndLock(t)

	child := exec.Command(os.Args[0], "-test.run=^TestExperimentalJournalLockSurvivesKill9$")
	child.Env = append(os.Environ(),
		"GARM_CLI_EXP_LOCK_KILL_CHILD=1",
		"GARM_CLI_EXP_PENDING_PATH="+experimentalPendingPath(),
	)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatalf("could not capture child stdout: %v", err)
	}
	if err := child.Start(); err != nil {
		t.Fatalf("could not start the lock-holder child: %v", err)
	}

	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(stdout).ReadString('\n')
		line <- s
	}()
	select {
	case got := <-line:
		if got != "child-holding\n" {
			t.Fatalf("child did not announce lock ownership, got %q", got)
		}
	case <-time.After(30 * time.Second):
		_ = child.Process.Kill()
		t.Fatal("timed out waiting for the child to acquire the journal lock")
	}

	// kill -9: no userspace cleanup is possible. The kernel must release the
	// advisory lock with the process.
	if err := child.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("could not kill -9 the lock holder: %v", err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("child must have been killed")
	}

	release, err := acquireExperimentalJournalLock()
	if err != nil {
		t.Fatalf("the lock must be immediately acquirable after kill -9, got: %v", err)
	}
	release()
}

func TestExperimentalJournalLockUnreadableRecordStillRefuses(t *testing.T) {
	clearJournalAndLock(t)
	release, err := acquireExperimentalJournalLock()
	if err != nil {
		t.Fatalf("first acquisition failed: %v", err)
	}
	defer release()

	// Corrupt the diagnostics record while this process holds the kernel
	// lock: acquisition is enforced by the kernel, so a contender must still
	// be refused — it just cannot report who holds it.
	lockPath := experimentalJournalLockPath()
	if err := os.WriteFile(lockPath, []byte("not-json"), 0o600); err != nil {
		t.Fatalf("could not corrupt the lock record: %v", err)
	}

	_, err = acquireExperimentalJournalLock()
	if err == nil {
		t.Fatal("a held lock must fail closed even when its record is unreadable")
	}
	if !strings.Contains(err.Error(), "unreadable") {
		t.Errorf("expected the refusal to name the unreadable record, got: %v", err)
	}
	if _, statErr := os.Stat(lockPath); statErr != nil {
		t.Errorf("the lock file must survive a refused acquisition, stat err: %v", statErr)
	}
}

func TestPoolSettingsPatchRefusesBindingMismatchApply(t *testing.T) {
	withProfile(t, "profile-a")

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cli := newTestRawClient(srv)
	_, currentServer := experimentalCurrentBinding(cli)

	cases := []struct {
		name     string
		mutate   func(*experimentalPendingRecord)
		wantText []string
	}{
		{
			name:     "profile mismatch",
			mutate:   func(rec *experimentalPendingRecord) { rec.Profile = "profile-b" },
			wantText: []string{`profile "profile-b"`, `"profile-a"`},
		},
		{
			name:     "server mismatch",
			mutate:   func(rec *experimentalPendingRecord) { rec.Server = "http://127.0.0.1:1/api/v1" },
			wantText: []string{`server "http://127.0.0.1:1/api/v1"`},
		},
		{
			name:     "entity mismatch",
			mutate:   func(rec *experimentalPendingRecord) { rec.EntityID = "repo-9" },
			wantText: []string{`repository "repo-9"`},
		},
		{
			name:     "pool mismatch",
			mutate:   func(rec *experimentalPendingRecord) { rec.PoolID = "pool-9" },
			wantText: []string{`pool "pool-9"`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := experimentalPendingRecord{
				RequestID:  "rec-bound-1",
				EntityType: "repository",
				EntityID:   "repo-1",
				PoolID:     "pool-1",
				ETag:       `"100:8"`,
				Payload:    `{"priority":7}`,
				At:         "2026-11-16T10:00:00Z",
				Profile:    "profile-a",
				Server:     currentServer,
			}
			tc.mutate(&rec)
			seedPendingRecord(t, rec)

			_, err := executeExperimental(cli, "patch", "--repo", "repo-1", "--pool", "pool-1",
				"--etag", `"100:9"`, "--priority", "9")
			if err == nil {
				t.Fatal("expected binding mismatch refusal, got nil")
			}
			msg := err.Error()
			for _, want := range tc.wantText {
				if !strings.Contains(msg, want) {
					t.Errorf("expected refusal to mention %q, got: %v", want, err)
				}
			}
			if !strings.Contains(msg, "rec-bound-1") {
				t.Errorf("expected refusal to name the pending request, got: %v", err)
			}
			if calls.Load() != 0 {
				t.Errorf("binding mismatch must refuse before any network dispatch, got %d calls", calls.Load())
			}
			data, readErr := os.ReadFile(experimentalPendingPath())
			if readErr != nil {
				t.Fatalf("pending journal must survive the refusal: %v", readErr)
			}
			var kept experimentalPendingRecord
			if uErr := json.Unmarshal(data, &kept); uErr != nil || kept.RequestID != "rec-bound-1" {
				t.Fatalf("pending journal must be untouched by the refusal, got %+v (err %v)", kept, uErr)
			}
		})
	}
}

func TestPoolSettingsPatchResolveRefusesBindingMismatch(t *testing.T) {
	withProfile(t, "profile-a")

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cli := newTestRawClient(srv)
	_, currentServer := experimentalCurrentBinding(cli)

	cases := []struct {
		name     string
		profile  string
		server   string
		wantText []string
	}{
		{
			name:     "profile mismatch",
			profile:  "profile-b",
			server:   currentServer,
			wantText: []string{"refusing to resolve", `profile "profile-b"`, `"profile-a"`},
		},
		{
			name:     "server mismatch",
			profile:  "profile-a",
			server:   "http://127.0.0.1:1/api/v1",
			wantText: []string{"refusing to resolve", `server "http://127.0.0.1:1/api/v1"`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seedPendingRecord(t, experimentalPendingRecord{
				RequestID:  "rec-bound-2",
				EntityType: "repository",
				EntityID:   "repo-1",
				PoolID:     "pool-1",
				ETag:       `"100:8"`,
				Payload:    `{"priority":7}`,
				At:         "2026-11-16T10:00:00Z",
				Profile:    tc.profile,
				Server:     tc.server,
			})

			_, err := executeExperimental(cli, "patch", "--resolve")
			if err == nil {
				t.Fatal("expected binding mismatch refusal on resolve, got nil")
			}
			for _, want := range tc.wantText {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("expected refusal to mention %q, got: %v", want, err)
				}
			}
			if calls.Load() != 0 {
				t.Errorf("binding mismatch must refuse before any network dispatch, got %d calls", calls.Load())
			}
		})
	}
}

// TestPoolSettingsPendingRecordSurvivesRestart dispatches a write whose
// response is lost (UNKNOWN hold), then simulates a process restart: the
// in-memory command state is discarded (fresh command tree and reset flags),
// the record is re-read from disk, and --resolve re-presents the exact
// journaled request against the same profile/server binding.
func TestPoolSettingsPendingRecordSurvivesRestart(t *testing.T) {
	withProfile(t, "profile-restart")
	clearJournalAndLock(t)

	var sawRequestID atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		sawRequestID.Store(r.Header.Get("Idempotency-Key"))
		w.Header().Set("ETag", `"100:9"`)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"priority":7,"max_runners":10,"min_idle_runners":1}`)
	}))
	defer srv.Close()

	// First process: the response is lost after dispatch, so the journal
	// holds the exact request.
	doomedCli := &rawClient{
		BaseURL: srv.URL + "/api/v1",
		Token:   "test-token",
		HTTPClient: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("connection reset by peer")
		})},
	}
	_, err := executeExperimental(doomedCli, "patch", "--repo", "repo-1", "--pool", "pool-1",
		"--etag", `"100:8"`, "--priority", "7", "--request-id", "restart-req-1")
	if err == nil {
		t.Fatal("expected UNKNOWN hold on lost response")
	}

	// Simulated restart: re-read the journal from disk with no in-memory
	// state carried over.
	rec, hasPending, err := loadExperimentalPending()
	if err != nil || !hasPending {
		t.Fatalf("record must survive the restart: hasPending=%v err=%v", hasPending, err)
	}
	profile, server := experimentalCurrentBinding(newTestRawClient(srv))
	if rec.RequestID != "restart-req-1" || rec.Profile != profile || rec.Server != server || rec.Payload != `{"priority":7}` || rec.ETag != `"100:8"` {
		t.Fatalf("record did not survive the restart intact: %+v", rec)
	}

	// Second process (fresh command tree): --resolve succeeds because the
	// current binding matches the journaled one.
	cli := newTestRawClient(srv)
	_, err = executeExperimental(cli, "patch", "--resolve")
	if err != nil {
		t.Fatalf("resolve after restart must succeed against the same binding: %v", err)
	}
	if got := sawRequestID.Load(); got != "restart-req-1" {
		t.Errorf("expected the server to see the journaled request id, got %v", got)
	}
}

// TestExperimentalJournalLockExcludedAcrossProcesses is the true
// multi-process check: while the parent (this test process) holds the
// journal lock, a second, real process — the test binary re-executing itself
// as a helper child via the standard env-var re-exec pattern — must fail to
// acquire it, and must not be able to journal. Once the parent releases, the
// same child re-run must acquire immediately (the kernel released the lock;
// the persistent lock file is not stale state).
func TestExperimentalJournalLockExcludedAcrossProcesses(t *testing.T) {
	mode := os.Getenv("GARM_CLI_EXP_LOCK_CHILD_MODE")
	if mode != "" {
		experimentalJournalLockChild(mode)
		return
	}

	clearJournalAndLock(t)
	release, err := acquireExperimentalJournalLock()
	if err != nil {
		t.Fatalf("parent could not acquire the journal lock: %v", err)
	}

	if err := writeExperimentalPending(experimentalPendingRecord{
		RequestID:  "parent-record",
		EntityType: "repository",
		EntityID:   "repo-1",
		PoolID:     "pool-1",
		ETag:       `"100:8"`,
		Payload:    `{"priority":7}`,
		At:         "2026-11-16T10:00:00Z",
	}); err != nil {
		t.Fatalf("parent could not journal: %v", err)
	}

	child := exec.Command(os.Args[0], "-test.run=^TestExperimentalJournalLockExcludedAcrossProcesses$")
	child.Env = append(os.Environ(),
		"GARM_CLI_EXP_LOCK_CHILD_MODE=contend",
		"GARM_CLI_EXP_PENDING_PATH="+experimentalPendingPath(),
	)
	out, runErr := child.CombinedOutput()
	if runErr != nil {
		t.Fatalf("child must fail closed while the parent holds the lock; child err: %v, output: %s", runErr, out)
	}
	if !strings.Contains(string(out), "locked by garm-cli process") {
		t.Errorf("expected the child to report the live holder, got: %s", out)
	}

	// The parent must still be the only process that journaled.
	data, err := os.ReadFile(experimentalPendingPath())
	if err != nil {
		t.Fatalf("parent record must survive the child's attempt: %v", err)
	}
	var rec experimentalPendingRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("journal unreadable after the child attempt: %v", err)
	}
	if rec.RequestID != "parent-record" {
		t.Errorf("child overwrote the journal: %+v", rec)
	}

	// Phase 2: after the parent releases, the lock must be immediately
	// acquirable by another process.
	release()

	lockPath := experimentalJournalLockPath()
	if _, statErr := os.Stat(lockPath); statErr != nil {
		t.Fatalf("the persistent lock file must survive release: %v", statErr)
	}

	acquirer := exec.Command(os.Args[0], "-test.run=^TestExperimentalJournalLockExcludedAcrossProcesses$")
	acquirer.Env = append(os.Environ(),
		"GARM_CLI_EXP_LOCK_CHILD_MODE=acquire",
		"GARM_CLI_EXP_PENDING_PATH="+experimentalPendingPath(),
	)
	out, runErr = acquirer.CombinedOutput()
	if runErr != nil {
		t.Fatalf("child must acquire the lock once the parent released it; child err: %v, output: %s", runErr, out)
	}
	if !strings.Contains(string(out), "child-acquired") {
		t.Errorf("expected the child to report acquisition, got: %s", out)
	}
	data, err = os.ReadFile(experimentalPendingPath())
	if err != nil {
		t.Fatalf("journal unreadable after the child acquisition: %v", err)
	}
	if err := json.Unmarshal(data, &rec); err != nil || rec.RequestID != "child-record" {
		t.Errorf("child must have journaled its own record after acquiring the lock, got %+v (err %v)", rec, err)
	}
}

// experimentalJournalLockChild is the helper-process entry point for
// TestExperimentalJournalLockExcludedAcrossProcesses. In "contend" mode the
// lock is expected to be held by the parent: refusal on stderr with exit 0
// is success, acquiring means the lock was not exclusive across processes.
// In "acquire" mode the lock is expected to be free: the child acquires,
// journals a child record, releases and exits 0.
func experimentalJournalLockChild(mode string) {
	if p := os.Getenv("GARM_CLI_EXP_PENDING_PATH"); p != "" {
		experimentalPendingPathOverride = p
	}
	release, err := acquireExperimentalJournalLock()
	if err != nil {
		if mode == "contend" {
			fmt.Fprintf(os.Stderr, "child refused as expected: %v\n", err)
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "child could not acquire the released lock: %v\n", err)
		os.Exit(1)
	}
	_ = writeExperimentalPending(experimentalPendingRecord{
		RequestID:  "child-record",
		EntityType: "repository",
		EntityID:   "repo-1",
		PoolID:     "pool-1",
		ETag:       `"100:8"`,
		Payload:    `{"priority":9}`,
	})
	release()
	switch mode {
	case "contend":
		fmt.Fprintln(os.Stderr, "child unexpectedly acquired the journal lock and journaled")
		os.Exit(1)
	case "acquire":
		fmt.Fprintln(os.Stderr, "child-acquired")
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown child mode %q\n", mode)
		os.Exit(1)
	}
}

// experimentalJournalLockHoldChild is the helper-process entry point for
// TestExperimentalJournalLockSurvivesKill9: it acquires the journal lock,
// announces ownership on stdout and blocks until killed, so the parent can
// SIGKILL it and prove the kernel released the advisory lock.
func experimentalJournalLockHoldChild() {
	if p := os.Getenv("GARM_CLI_EXP_PENDING_PATH"); p != "" {
		experimentalPendingPathOverride = p
	}
	release, err := acquireExperimentalJournalLock()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hold-child could not acquire the journal lock: %v\n", err)
		os.Exit(1)
	}
	// Held until the process is killed; release exists only so the fd is
	// clearly owned for the lifetime of the process.
	defer release()
	fmt.Fprintln(os.Stdout, "child-holding")
	for {
		time.Sleep(time.Hour)
	}
}
