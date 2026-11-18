//go:build linux

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassettransfer"
)

func run(ctx context.Context) error {
	path := flag.String("config", "", "Protected canonical deployment configuration")
	mode := flag.String("mode", "review", "review, transfer, or inspect; never activate")
	review := flag.String("approved-plan-sha256", "", "Exact independently approved plan digest required for remote operations")
	flag.Parse()
	if flag.NArg() != 0 || *path == "" || (*mode != "review" && *mode != "transfer" && *mode != "inspect") {
		return nativeassettransfer.ErrInvalid
	}
	c, e := nativeassettransfer.LoadConfiguration(*path)
	if e != nil {
		return e
	}
	b, e := nativeassettransfer.LoadBundle(c.BundleDirectory, c.HelperPath, c.ManifestSHA256, c.HelperSHA256)
	if e != nil {
		return e
	}
	defer b.Close()
	p, e := b.Plan(c.Target)
	if e != nil {
		return e
	}
	if *mode == "review" {
		if *review != "" {
			return nativeassettransfer.ErrInvalid
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			Plan                 nativeassettransfer.Plan `json:"plan"`
			Digest               string                   `json:"review_digest"`
			ActivationAuthorized bool                     `json:"activation_authorized"`
		}{p, nativeassettransfer.Digest(p), false})
	}
	if *review != nativeassettransfer.Digest(p) {
		return nativeassettransfer.ErrInvalid
	}
	key, e := c.LoadSigner()
	if e != nil {
		return e
	}
	if *mode == "inspect" {
		r, e := b.Inspect(ctx, p, *review, key, c.JournalDirectory)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(r)
	}
	r, e := b.Transfer(ctx, p, *review, key, c.JournalDirectory)
	if e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(r)
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if run(ctx) != nil {
		fmt.Fprintln(os.Stderr, "Runtime asset transfer unavailable or unknown; preserve the journal and inspect the same approved ID. No activation authorized.")
		os.Exit(1)
	}
}
