//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativepreflight"
	"io"
	"os"
)

func run(args []string, out io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("capacity-native-preflight accepts no arguments and performs read-only diagnostics")
	}
	r, e := nativepreflight.Collect(context.Background())
	if e != nil {
		return e
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
func main() {
	if e := run(os.Args[1:], os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
