// capacity-demand-observer is opt-in, read-only, and never starts providers or
// updates pool limits. It does not install itself as a service.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	config := flag.String("config", "", "private observer configuration file")
	once := flag.Bool("once", false, "perform one observation and exit")
	flag.Parse()
	c, e := demandobserver.Load(*config)
	if e != nil || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "read-only demand observer configuration unavailable")
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e = demandobserver.Run(ctx, c, os.Stdout, *once); e != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "read-only demand observer unavailable")
		os.Exit(1)
	}
}
