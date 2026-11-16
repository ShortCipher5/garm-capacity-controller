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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
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

// resetExpFlags clears the package-level flag backing variables so state
// cannot bleed between executions of the experimental command tree.
func resetExpFlags() {
	expPoolEntityRepo = ""
	expPoolEntityOrg = ""
	expPoolEntityEnterprise = ""
	expPoolID = ""
	expPoolSettingsETag = ""
	expPoolSettingsDraftFile = ""
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

func writeTempDraft(t *testing.T, content string) string {
	t.Helper()
	f := t.TempDir() + "/draft.json"
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write draft file: %v", err)
	}
	return f
}
