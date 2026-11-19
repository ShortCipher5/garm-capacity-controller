//go:build linux

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassets"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	source := flag.String("prepare-source", "", "Protected flat approved artifact bundle; fresh preparation only")
	id := flag.String("inspect-id", "", "Read-only completed preparation ID; never retry partial writes")
	want := flag.String("expected-manifest-sha256", "", "Independently approved exact manifest digest")
	flag.Parse()
	if flag.NArg() != 0 || (*source == "") == (*id == "") {
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	var r nativeassets.Receipt
	var e error
	if *source != "" {
		r, e = nativeassets.Prepare(ctx, *source, *want)
	} else {
		r, e = nativeassets.Inspect(ctx, *id, *want)
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, nativeassets.ErrAssets)
		os.Exit(1)
	}
	json.NewEncoder(os.Stdout).Encode(r)
}
