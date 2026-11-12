// garm-journal-init explicitly initializes a NEW management journal. The normal
// gateway never initializes or adopts missing, empty, replaced or legacy state.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
)

func run(args []string, out, errOut io.Writer) int {
	flags := flag.NewFlagSet("garm-journal-init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("journal", "", "NEW journal file under an existing private owner-owned directory; never an existing or recovery path")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(out, "Usage: garm-journal-init -journal NEW-PATH (preexisting private owner-owned directory required)")
			return 0
		}
		fmt.Fprintln(errOut, "invalid initialization arguments")
		return 2
	}
	if *path == "" || flags.NArg() != 0 {
		fmt.Fprintln(errOut, "invalid initialization arguments")
		return 2
	}
	journal, err := garmmanagement.InitializeJournal(*path)
	if err != nil {
		fmt.Fprintln(errOut, "new journal initialization refused; do not delete or replace existing history to bypass this failure")
		return 1
	}
	lineage := journal.Lineage()
	if journal.Close() != nil {
		fmt.Fprintln(errOut, "journal outcome unavailable; do not retry initialization or replace the file")
		return 1
	}
	if json.NewEncoder(out).Encode(map[string]string{"journal_lineage": lineage}) != nil {
		fmt.Fprintln(errOut, "journal created but lineage output failed; do not retry initialization or replace the file")
		return 1
	}
	return 0
}
func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
