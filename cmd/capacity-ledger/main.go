// capacity-ledger is a local operator tool, not a live recovery orchestrator.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

const usage = `Usage:
  capacity-ledger backup --source EXISTING_LEDGER --destination NEW_BACKUP
  capacity-ledger restore --source BACKUP --destination NEW_LEDGER --old-writers-fenced

Local SQLite online backup only. Private existing parent directories required.
Never overwrites a database or sidecar, starts a service, or enables admission.
Before restore, stop/fence every old writer, provider path, collector, and queued
request replay; preserve newer dispatch/cancellation history and ambiguous work.
--old-writers-fenced acknowledges operator action; this tool cannot verify it.
Before using the restored path, a NEW barrier followed by complete authoritative
Garm/Incus/in-flight-operation inventory is mandatory. Keep scheduling stopped.
`

// run keeps CLI validation ahead of all database access. In particular, restore
// has no ready/reconcile/start flag and never opens the published artifact.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if len(args) == 0 || (args[0] != "backup" && args[0] != "restore") {
		fmt.Fprint(stderr, usage)
		return 2
	}
	action := args[0]
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	// Do not echo malformed arguments or private local paths into diagnostics.
	flags.SetOutput(io.Discard)
	source := flags.String("source", "", "existing private local ledger or backup")
	destination := flags.String("destination", "", "new private local database path")
	var fenced bool
	if action == "restore" {
		flags.BoolVar(&fenced, "old-writers-fenced", false, "acknowledge all external old writers and queued request replay have been fenced")
	}
	if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, usage)
		return 0
	} else if err != nil || flags.NArg() != 0 || *source == "" || *destination == "" {
		fmt.Fprintln(stderr, "invalid arguments; use capacity-ledger --help")
		return 2
	}
	if action == "restore" && !fenced {
		fmt.Fprintln(stderr, "restore refused: stop/fence all old writers, provider paths, collectors and queued request replay, then acknowledge with --old-writers-fenced; this tool cannot verify external fencing")
		return 2
	}
	var err error
	if action == "backup" {
		err = admission.BackupSQLiteFile(ctx, *source, *destination)
	} else {
		err = admission.RestoreSQLite(ctx, *source, *destination)
	}
	if err != nil {
		return reportError(err, stderr)
	}
	fmt.Fprintln(stdout, action+" created: standalone private artifact; admission blocked; original source retained")
	if action == "restore" {
		fmt.Fprintln(stdout, "Keep external writers and queued request replay fenced and scheduling stopped. Before using this path, begin a NEW reconciliation barrier, then collect complete authoritative Garm/Incus/in-flight-operation inventory. This tool has not verified fencing, reconciled, or resumed a gateway.")
	}
	return 0
}

func reportError(err error, stderr io.Writer) int {
	switch {
	case errors.Is(err, admission.ErrUncertain):
		fmt.Fprintln(stderr, "export outcome uncertain: destination may have been published; preserve the source, inspect the destination offline, do not retry over it or enable admission; use a fresh destination for another attempt")
		return 3
	case errors.Is(err, os.ErrExist):
		fmt.Fprintln(stderr, "export refused: destination already exists; choose a new private path")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintln(stderr, "export interrupted; no successful artifact confirmed; preserve source and inspect destination before retrying")
	default:
		fmt.Fprintln(stderr, "export failed: verify existing private owned source, valid ledger, new destination without sidecars, private real parent directories and local filesystem access; no recovery or service start attempted")
	}
	return 1
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	cancel()
	os.Exit(code)
}
