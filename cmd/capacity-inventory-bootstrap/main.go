//go:build linux

// capacity-inventory-bootstrap is a preinstalled root-owned, fixed-purpose sudo
// helper. It has no shell, arbitrary path or arbitrary command input.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
	"io"
	"os"
	"time"
)

func main() {
	timer := time.AfterFunc(55*time.Second, func() { os.Exit(1) })
	defer timer.Stop()
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "arguments unsupported")
		os.Exit(1)
	}
	var r hostbootstrap.Request
	d := json.NewDecoder(io.LimitReader(os.Stdin, 24<<20))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF {
		fmt.Fprintln(os.Stderr, "invalid bounded request")
		os.Exit(1)
	}
	if e := hostbootstrap.Install(context.Background(), r); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	fmt.Printf("inventory-bootstrap-installed %s\n", r.ReviewDigest)
}
