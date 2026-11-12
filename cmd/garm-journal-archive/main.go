// garm-journal-archive is a local backup/inspection tool, never a live recovery
// orchestrator or a way to activate historical remote-mutation history.
package main

import (
	"context"
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

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
)

const usage = `Usage:
  garm-journal-archive backup --source ACTIVE_JOURNAL --destination NEW_ARCHIVE --lineage EXPECTED_ID
  garm-journal-archive restore-inspection --source ARCHIVE --destination NEW_COPY --lineage EXPECTED_ID
  garm-journal-archive inspect --source ARCHIVE [--lineage EXPECTED_ID]

Existing owner-private files and real private parent directories are required.
Backup uses SQLite's online snapshot API, including committed WAL contents.
Never overwrites a file/sidecar, initializes a journal, contacts Garm, clears an
intent or resumes mutations. Archives and inspection restores have a distinct
application ID and are deliberately rejected by the normal journal opener.
A historical snapshot can omit later accepted writes even if pending is zero.
External request-completion fencing and complete newer history are not proved.
There is no activate/force/rollback/retry option.
`

func validLineage(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if len(args) == 0 || (args[0] != "backup" && args[0] != "restore-inspection" && args[0] != "inspect") {
		fmt.Fprint(stderr, usage)
		return 2
	}
	action := args[0]
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("source", "", "existing local private source")
	destination := flags.String("destination", "", "new local private destination")
	lineage := flags.String("lineage", "", "independently configured journal lineage")
	if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, usage)
		return 0
	} else if err != nil || flags.NArg() != 0 || *source == "" || (action != "inspect" && (*destination == "" || !validLineage(*lineage))) || (action == "inspect" && (*destination != "" || (*lineage != "" && !validLineage(*lineage)))) {
		fmt.Fprintln(stderr, "invalid arguments; use garm-journal-archive --help")
		return 2
	}
	var info garmmanagement.ArchiveInfo
	var err error
	switch action {
	case "backup":
		info, err = garmmanagement.BackupFile(ctx, *source, *destination, *lineage)
	case "restore-inspection":
		info, err = garmmanagement.RestoreInspection(ctx, *source, *destination, *lineage)
	case "inspect":
		info, err = garmmanagement.InspectArchive(ctx, *source)
		if err == nil && *lineage != "" && info.JournalLineage != *lineage {
			err = garmmanagement.ErrArchive
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, garmmanagement.ErrArchiveUncertain):
			fmt.Fprintln(stderr, "archive publication uncertain: preserve the source and inspect the destination; it may already exist; never overwrite it or resume mutations")
			return 3
		case errors.Is(err, os.ErrExist):
			fmt.Fprintln(stderr, "destination already exists; no overwrite attempted; choose a new private path")
		case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
			fmt.Fprintln(stderr, "archive operation interrupted; preserve source and inspect destination before another attempt; no recovery or activation performed")
		default:
			fmt.Fprintln(stderr, "archive operation refused: verify private existing source, matching configured lineage, supported schema and new private destination without sidecars; no activation or remote operation attempted")
		}
		return 1
	}
	if json.NewEncoder(stdout).Encode(info) != nil {
		fmt.Fprintln(stderr, "result output unavailable; inspect the destination; do not repeat over an existing path or enable mutations")
		return 3
	}
	fmt.Fprintln(stderr, "Inspection-only artifact. Normal service startup refuses it. This does not prove current remote state or permit resuming writes.")
	return 0
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	cancel()
	os.Exit(code)
}
