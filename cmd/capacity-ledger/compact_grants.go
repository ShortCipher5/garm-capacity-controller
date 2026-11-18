package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"io"
)

func compactGrants(ctx context.Context, args []string, out, diagnostic io.Writer) int {
	flags := flag.NewFlagSet("compact-grants", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("source", "", "existing private runtime ledger")
	identity := flags.String("identity-file", "", "independent private ledger identity")
	limit := flags.Int("limit", 20, "maximum terminal batches, 1..100")
	if e := flags.Parse(args); errors.Is(e, flag.ErrHelp) {
		fmt.Fprint(out, usage)
		return 0
	} else if e != nil || flags.NArg() != 0 || *source == "" || *identity == "" || *limit < 1 || *limit > 100 {
		fmt.Fprintln(diagnostic, "invalid compact-grants arguments; existing source and independent identity required")
		return 2
	}
	if ctx.Err() != nil {
		fmt.Fprintln(diagnostic, "grant compaction interrupted")
		return 1
	}
	store, e := admission.OpenRuntimeSQLite(*source, *identity)
	if e != nil {
		fmt.Fprintln(diagnostic, "existing runtime ledger identity or integrity unavailable")
		return 1
	}
	defer store.Close()
	result, e := store.CompactBrokerGrantHistory(ctx, *limit)
	if e != nil {
		fmt.Fprintln(diagnostic, "grant compaction incomplete; preserve ledger and inspect before retrying; no capacity release or activation requested")
		return 1
	}
	fmt.Fprintf(out, "Compacted %d terminal grant batches. Permanent replay/audit history retained; outstanding charges unchanged; no service activation.\n", len(result))
	return 0
}
