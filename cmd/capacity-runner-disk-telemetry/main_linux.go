// capacity-runner-disk-telemetry exposes only a reviewed, leased display read.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ShortCipher5/garm-capacity-controller/internal/remotedisktelemetry"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := remotedisktelemetry.ServeLocalInherited(ctx, os.Args[1:]); err != nil {
		if errors.Is(err, remotedisktelemetry.ErrLocalDaemonCustody) {
			fmt.Fprintln(os.Stderr, "independently reviewed daemon mount custody unavailable")
		} else {
			fmt.Fprintln(os.Stderr, "reviewed local runner disk telemetry unavailable")
		}
		os.Exit(1)
	}
}
