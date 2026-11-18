//go:build linux

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprepare"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--internal-check-owner" {
		if nativeprepare.CheckChild(os.Args[2:]) != nil {
			fmt.Fprintln(os.Stderr, nativeprepare.ErrPreparation)
			os.Exit(1)
		}
		return
	}
	source := flag.String("source", "", "root-owned deployment-loaded preparation bundle")
	inspect := flag.String("inspect", "", "read exact completed preparation ID only")
	flag.Parse()
	if flag.NArg() != 0 || (*source == "") == (*inspect == "") {
		fmt.Fprintln(os.Stderr, nativeprepare.ErrPreparation)
		os.Exit(1)
	}
	var r nativeprepare.Receipt
	var e error
	if *inspect != "" {
		r, e = nativeprepare.Inspect(context.Background(), *inspect)
	} else {
		r, e = nativeprepare.Prepare(context.Background(), *source)
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, nativeprepare.ErrPreparation)
		os.Exit(1)
	}
	json.NewEncoder(os.Stdout).Encode(r)
}
