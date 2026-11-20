// capacity-stock-read-pilot is an explicit, time-bounded qualification caller.
// It is not installed or enabled by the normal gateway/writer packaging.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ShortCipher5/garm-capacity-controller/internal/managedgarm"
)

func run(ctx context.Context, args []string) error {
	if ctx == nil || ctx.Err() != nil || len(args) != 1 || args[0] != "serve-existing-readonly" {
		return managedgarm.ErrBoundary
	}
	return managedgarm.RunStockReadQualification(ctx)
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if run(ctx, os.Args[1:]) != nil {
		fmt.Fprintln(os.Stderr, "stock read pilot stopped or unavailable; no activation or qualification proof issued")
		os.Exit(1)
	}
}
