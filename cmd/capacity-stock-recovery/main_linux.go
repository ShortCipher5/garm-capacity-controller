// capacity-stock-recovery operates on an existing local stock-apply journal.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/brokerinput"
)

const usage = `Usage: capacity-stock-recovery inspect|pause|resume --journal FILE --lineage HEX --identity HEX --scope HEX [--expect MODE_REVISION --reason TEXT]

Supply the existing private journal, its independently configured lineage and
bound controller identity, and the reviewed scope digest. Inspect reports
local admission mode and any open operation with its last recorded charge.
Pause and resume require the mode_revision from inspect and an operator reason.
Resume only lifts the local admission pause. After a lost response, inspect
again before deciding whether another change is needed.
`

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if len(args) == 0 || (args[0] != "inspect" && args[0] != "pause" && args[0] != "resume") {
		fmt.Fprint(stderr, usage)
		return 2
	}
	action := args[0]
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("journal", "", "existing bound stock-apply journal")
	lineage := flags.String("lineage", "", "expected journal lineage")
	identity := flags.String("identity", "", "expected controller identity")
	scopeID := flags.String("scope", "", "reviewed scope digest")
	var expected, reason string
	if action != "inspect" {
		flags.StringVar(&expected, "expect", "", "reviewed mode revision")
		flags.StringVar(&reason, "reason", "", "operator reason for the local change")
	}
	err := flags.Parse(args[1:])
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if err != nil || flags.NArg() != 0 || *path == "" || !validDigest(*lineage) || !validDigest(*identity) || !validDigest(*scopeID) ||
		*scopeID == strings.Repeat("0", sha256.Size*2) || (action != "inspect" && (!validDigest(expected) || strings.TrimSpace(reason) == "")) {
		fmt.Fprint(stderr, usage)
		return 2
	}
	raw, _ := hex.DecodeString(*scopeID)
	scope := [sha256.Size]byte(raw)
	journal, err := brokerinput.OpenStockApplyJournal(*path, *lineage)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer journal.Close()
	controller, err := brokerinput.OpenStockApplyController(journal, *identity)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var result brokerinput.StockApplyRecovery
	if action == "inspect" {
		result, err = controller.RecoverScope(ctx, scope, time.Now())
	} else {
		result, err = controller.ChangeScopePause(ctx, scope, expected, action == "pause", reason, time.Now())
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		if action != "inspect" {
			fmt.Fprintln(stderr, "Inspect this scope before making another mode decision.")
		}
		return 1
	}
	summary := scopeSummary{Scope: result.Scope, ModeRevision: result.ModeRevision, LocalAdmissionPaused: result.Paused, Inflight: result.Inflight, ListenerDrain: "not_observed", UpstreamOutcome: "not_observed"}
	if op := result.Operation; op != nil {
		summary.OperationID = op.OperationID
		summary.LastRecordedCharge = &op.Charged
		if result.Inflight == brokerinput.StockApplyInflightOutcomeUnresolved {
			summary.UpstreamOutcome = "unresolved"
		}
	}
	if err := json.NewEncoder(stdout).Encode(summary); err != nil {
		fmt.Fprintln(stderr, "Result output unavailable. Inspect this scope to read its committed state.")
		return 1
	}
	return 0
}

// Keep settings payloads, free-form reasons and source identifiers out of the
// command output. A missing operation carries no charge measurement.
type scopeSummary struct {
	Scope                string `json:"scope"`
	ModeRevision         string `json:"mode_revision"`
	LocalAdmissionPaused bool   `json:"local_admission_paused"`
	Inflight             string `json:"inflight"`
	OperationID          string `json:"operation_id,omitempty"`
	LastRecordedCharge   *int64 `json:"last_recorded_charge,omitempty"`
	ListenerDrain        string `json:"listener_drain"`
	UpstreamOutcome      string `json:"upstream_outcome"`
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	cancel()
	os.Exit(code)
}
