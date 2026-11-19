//go:build stock_read_pilot_harness

package managedgarm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Only the independently approved, explicitly selected hosted specimen may run
// this operation. No URL, directory, namespace, container or credential selector
// is accepted. This file is absent from normal builds and normal test suites.
func TestHostedStockReadPilotBootstrap(t *testing.T) {
	selected, selectionErr := stockPilotHarnessSelection(os.Getenv("GARM_STOCK_READ_PILOT_TEST"), os.Getenv("GITHUB_ACTIONS"), os.Getenv("RUNNER_ENVIRONMENT"))
	if selectionErr != nil {
		t.Fatal("refusing unapproved hosted pilot environment")
	}
	if !selected {
		t.Skip("disposable stock pilot bootstrap not selected")
	}
	if os.Geteuid() != 0 {
		t.Fatal("refusing non-root pilot harness")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	profile, err := loadStockReadProfile()
	if err != nil || profile.Directory != stockPilotHarnessDirectory {
		t.Fatal("fixed reviewed pilot profile unavailable")
	}
	// Reuse the actual guarded provisional reader before checking fresh state.
	store, err := openStockPilotBootstrapStore(ctx, profile)
	if err != nil {
		t.Fatal("protected provisional pilot material unavailable")
	}
	for _, name := range []string{"bootstrap-attempt.json", "bootstrap-record.json", "stock-auth-binding.json"} {
		if _, err := store.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			store.root.Close()
			t.Fatal("pilot is not fresh; refusing setup or replay")
		}
	}
	store.root.Close()
	before, err := stockPilotHarnessNamespaceFDs()
	if err != nil {
		t.Fatal("descriptor baseline unavailable")
	}
	if bootstrapStockReadPilot(ctx) != nil {
		t.Fatal("bootstrap did not confirm completion; outcome may be unknown, no automatic replay")
	}
	// This confirms exact preboot listeners remain; it does not admit a reader.
	if _, err := observeStockReadOnly(ctx); err != nil {
		t.Fatal("post-bootstrap preboot verification unavailable")
	}
	after, err := stockPilotHarnessNamespaceFDs()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("network namespace descriptor counts not restored")
	}
	// Reopen the same protected store, then verify only actual persisted lineage.
	store, err = openStockPilotBootstrapStore(ctx, profile)
	if err != nil {
		t.Fatal("completed protected store unavailable")
	}
	defer store.root.Close()
	var attempt bootstrapAttempt
	var record bootstrapRecord
	if store.read("bootstrap-attempt.json", &attempt) != nil || store.read("bootstrap-record.json", &record) != nil || record.bootstrapAttempt != attempt || attempt.GarmID != profile.IDs[0] || attempt.CredentialDigest != store.review.CredentialDigest || !stockUUID(record.ControllerID) || record.UserID != "" {
		t.Fatal("actual completed lineage verification failed")
	}
	// Compute, validate and immediately clear the completion commitment from
	// these exact protected persisted bytes. Never install or emit the result.
	names := [4]string{"stock-api.der", "bootstrap-user.json", "bootstrap-attempt.json", "bootstrap-record.json"}
	var rawFiles [4][]byte
	defer func() {
		for _, raw := range rawFiles {
			clear(raw)
		}
	}()
	for i, name := range names {
		rawFiles[i], err = stockReadAuthFile(store.root, name)
		if err != nil {
			t.Fatal("actual completion input unavailable")
		}
	}
	var actualAttempt bootstrapAttempt
	var actualRecord bootstrapRecord
	provisional, _ := json.Marshal(store.review)
	if !stockBindingJSON(rawFiles[2], &actualAttempt) || actualAttempt != attempt || !stockBindingJSON(rawFiles[3], &actualRecord) || actualRecord != record {
		t.Fatal("persisted completion lineage drift")
	}
	if _, _, err := validateStockPilotBootstrap(profile.IDs[0], [3][]byte{provisional, rawFiles[0], rawFiles[1]}); err != nil {
		t.Fatal("completion material differs from approved provisional binding")
	}
	completed, err := stockPilotFixtureCompleted(profile, rawFiles[0], rawFiles[1], rawFiles[2], rawFiles[3])
	defer clear(completed.Bytes)
	if err != nil || completed.Path != stockPilotHarnessDirectory+"/supervisor-admin/stock-auth-binding.json" || completed.UID != 0 || completed.Mode != 0600 {
		t.Fatal("computed completion commitment invalid")
	}
	for i, name := range names {
		again, e := stockReadAuthFile(store.root, name)
		same := e == nil && bytes.Equal(again, rawFiles[i])
		clear(again)
		if !same {
			t.Fatal("completion input changed during review")
		}
	}
	finalProfile, e := loadStockReadProfile()
	if e != nil || finalProfile != profile || store.check() != nil || ctx.Err() != nil {
		t.Fatal("completion review context changed")
	}
	report := struct {
		Schema                       string `json:"schema"`
		BootstrapConfirmed           bool   `json:"bootstrap_confirmed"`
		ExactPrebootListeners        bool   `json:"exact_preboot_listeners_verified"`
		NamespaceFDCountsRestored    bool   `json:"namespace_descriptor_counts_restored"`
		CompletionCommitmentVerified bool   `json:"completion_commitment_verified"`
		ReadAdmission                bool   `json:"read_admission"`
		Serving                      bool   `json:"serving"`
		DeploymentAuthorized         bool   `json:"deployment_authorized"`
	}{"disposable-stock-pilot-bootstrap/v1", true, true, true, true, false, false, false}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal("public report encoding failed")
	}
	t.Log("STOCK_READ_PILOT_BOOTSTRAP_PUBLIC " + string(raw))
}

// Diagnostic own-process descriptor counts only. No raw namespace IDs are
// emitted. Equality does not prove exclusive custody or general host cleanup.
func stockPilotHarnessNamespaceFDs() (map[string]int, error) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil || len(entries) > 4096 {
		return nil, ErrBoundary
	}
	out := map[string]int{}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, ErrBoundary
		}
		if strings.HasPrefix(target, "net:[") && strings.HasSuffix(target, "]") {
			out[target]++
		}
	}
	return out, nil
}
