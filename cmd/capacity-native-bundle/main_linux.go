//go:build linux

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativebundle"
	"os"
)

func main() {
	var c nativebundle.Configuration
	flag.StringVar(&c.ExportDirectory, "export", "", "verified owner artifact export directory")
	flag.StringVar(&c.ExportManifestSHA256, "export-manifest-sha256", "", "explicit trusted SHA256SUMS digest")
	flag.StringVar(&c.PrivateDirectory, "private", "", "private capsule and TLS input directory")
	flag.StringVar(&c.OutputDirectory, "output", "", "fresh bundle directory")
	flag.StringVar(&c.ID, "id", "", "fresh 64-hex preparation ID")
	flag.StringVar(&c.Architecture, "architecture", "", "target amd64 or arm64")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, nativebundle.ErrBundle)
		os.Exit(1)
	}
	r, e := nativebundle.Build(context.Background(), c)
	if e != nil {
		fmt.Fprintln(os.Stderr, nativebundle.ErrBundle)
		os.Exit(1)
	}
	json.NewEncoder(os.Stdout).Encode(r)
}
