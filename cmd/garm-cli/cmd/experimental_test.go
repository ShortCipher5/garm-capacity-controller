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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

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

func TestExperimentalJournalLockFailsClosedWhileHeld(t *testing.T) {
	clearJournalAndLock(t)
	release, err := acquireExperimentalJournalLock()
	if err != nil {
		t.Fatalf("first acquisition failed: %v", err)
	}
	defer release()

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
}

func TestExperimentalJournalLockReclaimsDeadHolderOnce(t *testing.T) {
	clearJournalAndLock(t)

	// Reap a throwaway child so we hold a pid that provably no longer exists.
	dead := exec.Command(os.Args[0], "-test.run=^$")
	if err := dead.Run(); err != nil {
		t.Fatalf("could not spawn a throwaway process: %v", err)
	}
	deadPID := dead.Process.Pid
	if experimentalProcessAlive(deadPID) {
		t.Fatalf("test setup expected pid %d to be dead", deadPID)
	}

	lockPath := experimentalJournalLockPath()
	stale := []byte(fmt.Sprintf(`{"pid":%d,"acquired_at":"2026-11-16T10:00:00Z"}`, deadPID))
	if err := os.WriteFile(lockPath, stale, 0o600); err != nil {
		t.Fatalf("could not plant stale lock: %v", err)
	}

	release, err := acquireExperimentalJournalLock()
	if err != nil {
		t.Fatalf("stale lock held by dead pid %d must be reclaimed once, got: %v", deadPID, err)
	}
	defer release()

	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("reclaimed lock must record the new holder: %v", err)
	}
	var holder experimentalJournalLockRecord
	if err := json.Unmarshal(data, &holder); err != nil || holder.PID != os.Getpid() {
		t.Fatalf("lock record must name this process after reclaim, got pid=%d err=%v", holder.PID, err)
	}

	// The reclaimed lock is a real lock: a second acquisition fails closed.
	if _, err := acquireExperimentalJournalLock(); err == nil {
		t.Fatal("second acquisition must fail closed while the reclaimed lock is held")
	}
}

func TestExperimentalJournalLockUnreadableHolderFailsClosed(t *testing.T) {
	clearJournalAndLock(t)
	lockPath := experimentalJournalLockPath()
	t.Cleanup(func() { _ = os.Remove(lockPath) })

	if err := os.WriteFile(lockPath, []byte("not-json"), 0o600); err != nil {
		t.Fatalf("could not plant unreadable lock: %v", err)
	}
	_, err := acquireExperimentalJournalLock()
	if err == nil {
		t.Fatal("an unreadable lock record must fail closed, never be guessed or reclaimed")
	}
	if !strings.Contains(err.Error(), "unreadable") {
		t.Errorf("expected the refusal to name the unreadable record, got: %v", err)
	}
	if _, statErr := os.Stat(lockPath); statErr != nil {
		t.Errorf("unreadable lock must be left for the operator, stat err: %v", statErr)
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
// acquire it, and must not be able to journal.
func TestExperimentalJournalLockExcludedAcrossProcesses(t *testing.T) {
	if os.Getenv("GARM_CLI_EXP_LOCK_CHILD") == "1" {
		experimentalJournalLockChild()
		return
	}

	clearJournalAndLock(t)
	release, err := acquireExperimentalJournalLock()
	if err != nil {
		t.Fatalf("parent could not acquire the journal lock: %v", err)
	}
	defer release()

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
		"GARM_CLI_EXP_LOCK_CHILD=1",
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
}

// experimentalJournalLockChild is the helper-process entry point: it points
// the journal at the parent's path and tries to take the lock and journal.
// Exit 0 with the refusal on stderr is the expected outcome; exit 1 means the
// lock was not exclusive across processes.
func experimentalJournalLockChild() {
	if p := os.Getenv("GARM_CLI_EXP_PENDING_PATH"); p != "" {
		experimentalPendingPathOverride = p
	}
	release, err := acquireExperimentalJournalLock()
	if err != nil {
		fmt.Fprintf(os.Stderr, "child refused as expected: %v\n", err)
		os.Exit(0)
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
	fmt.Fprintln(os.Stderr, "child unexpectedly acquired the journal lock and journaled")
	os.Exit(1)
}
