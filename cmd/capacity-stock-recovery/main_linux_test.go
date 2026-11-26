package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/brokerinput"
)

type failedOutput struct{}

func (failedOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestRecoveredLocalDecisionAfterLostOutput(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "apply.sqlite")
	j, err := brokerinput.InitializeStockApplyJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	identity := strings.Repeat("a", 64)
	owner, err := brokerinput.NewStockApplyController(j, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	scope := sha256.Sum256([]byte("reviewed scope"))
	base := []string{"--journal", path, "--lineage", j.Lineage(), "--identity", identity, "--scope", hex.EncodeToString(scope[:])}
	args := func(action string, more ...string) []string {
		return append(append([]string{action}, base...), more...)
	}
	inspect := func() scopeSummary {
		t.Helper()
		var out, errs bytes.Buffer
		if code := run(ctx, args("inspect"), &out, &errs); code != 0 {
			t.Fatalf("inspect failed: %s", &errs)
		}
		var v scopeSummary
		if err := json.Unmarshal(out.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	initial := inspect()
	if initial.Scope != hex.EncodeToString(scope[:]) || initial.LocalAdmissionPaused || !validDigest(initial.ModeRevision) || initial.LastRecordedCharge != nil || initial.ListenerDrain != "not_observed" || initial.UpstreamOutcome != "not_observed" {
		t.Fatalf("initial summary: %+v", initial)
	}
	now := time.Now()
	plan := brokerinput.StockChangePlan{PoolID: "test-pool", RepositoryID: "fixture-repository", Source: "private-source", Fingerprint: "fixture-fingerprint", Scope: scope, Observed: 2, Suggested: 3, Delta: 1, Direction: "increase", ExpiresAt: now.Add(time.Minute)}
	if err := owner.RecordProposal(ctx, plan, 0, now); err != nil {
		t.Fatal(err)
	}
	stop := args("pause", "--expect", initial.ModeRevision, "--reason", "operator review")
	var errs bytes.Buffer
	if run(ctx, stop, failedOutput{}, &errs) == 0 || !strings.Contains(errs.String(), "Inspect") {
		t.Fatal("missing recovery instructions after losing the response", &errs)
	}
	paused := inspect()
	if !paused.LocalAdmissionPaused || paused.ModeRevision == initial.ModeRevision || paused.Inflight != brokerinput.StockApplyInflightProposalOpen || paused.OperationID == "" || paused.LastRecordedCharge == nil || *paused.LastRecordedCharge != 0 {
		t.Fatalf("after losing the stop reply: %+v", paused)
	}
	if err := owner.WithdrawProposal(ctx, scope, "attempted while paused", now); !errors.Is(err, brokerinput.ErrStockApplyPaused) {
		t.Fatalf("live controller missed local mode: %v", err)
	}
	var reply bytes.Buffer
	if run(ctx, args("resume", "--expect", paused.ModeRevision, "--reason", "lift local pause"), &reply, &errs) != 0 {
		t.Fatal(&errs)
	}
	if strings.Contains(reply.String(), plan.Source) || strings.Contains(reply.String(), plan.RepositoryID) || strings.Contains(reply.String(), "fingerprint") {
		t.Fatal("scope summary leaked journal details", &reply)
	}
	if run(ctx, stop, io.Discard, io.Discard) == 0 {
		t.Fatal("replayed original stop after resume")
	}
	after := inspect()
	if after.LocalAdmissionPaused || after.ModeRevision == paused.ModeRevision || after.OperationID != paused.OperationID || after.Inflight != paused.Inflight {
		t.Fatalf("after replay: %+v", after)
	}
	for _, invalid := range [][]string{
		args("pause"),
		args("rollback"),
		args("inspect", "--expect", after.ModeRevision),
		args("pause", "--expect", after.ModeRevision, "--reason", "test", "--identity", strings.Repeat("1", 64)),
		args("pause", "--expect", after.ModeRevision, "--reason", "test", "--lineage", strings.Repeat("1", 64)),
		args("pause", "--expect", after.ModeRevision, "--reason", "test", "--scope", strings.Repeat("1", 64)),
		args("pause", "--expect", after.ModeRevision, "--reason", "test", "--scope", strings.Repeat("0", 64)),
	} {
		if run(ctx, invalid, io.Discard, io.Discard) == 0 {
			t.Fatalf("accepted invalid command %q", invalid[0])
		}
	}
	missing := filepath.Join(dir, "missing.sqlite")
	if run(ctx, args("inspect", "--journal", missing), io.Discard, io.Discard) == 0 {
		t.Fatal("inspected missing journal")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("inspection created a journal", err)
	}
	if final := inspect(); final.ModeRevision != after.ModeRevision {
		t.Fatalf("invalid commands changed mode: %+v", final)
	}
}
