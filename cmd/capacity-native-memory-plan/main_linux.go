//go:build linux

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativepreflight"
	"os"
)

func main() {
	reserve := flag.Uint64("reserve-bytes", 0, "Explicit combined OS/host-services/ZFS planning allowance; not a hard limit")
	capsules := flag.Uint64("capsule-ceilings-bytes", 0, "Sum of all planned whole-capsule memory ceilings, not current usage")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	r, e := nativepreflight.InspectMemoryPlan(context.Background(), *reserve, *capsules)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	if json.NewEncoder(os.Stdout).Encode(r) != nil {
		os.Exit(1)
	}
	// Never use a successful shell exit as activation readiness.
	os.Exit(3)
}
