//go:build linux

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeimage"
	"io"
	"os"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--mkfs-exec" {
		if nativeimage.ExecMkfs() != nil {
			os.Exit(111)
		}
		return
	}
	var c nativeimage.Configuration
	var manifest string
	flag.StringVar(&manifest, "manifest", "", "Exact runtime inventory JSON")
	flag.StringVar(&c.Source, "source", "", "Root-owned supplied runtime filesystem")
	flag.StringVar(&c.Destination, "destination", "", "New exclusive output directory")
	flag.StringVar(&c.Mkfs, "mkfs", "", "Pinned mke2fs binary")
	flag.StringVar(&c.MkfsSHA256, "mkfs-sha256", "", "mke2fs SHA256")
	flag.StringVar(&c.Helper, "helper", "", "Pinned capacity-native-image executable")
	flag.StringVar(&c.HelperSHA256, "helper-sha256", "", "Image helper SHA256")
	flag.Parse()
	if flag.NArg() != 0 {
		os.Exit(2)
	}
	f, e := os.Open(manifest)
	if e != nil {
		fmt.Fprintln(os.Stderr, nativeimage.ErrImage)
		os.Exit(1)
	}
	d := json.NewDecoder(io.LimitReader(f, 8<<20))
	d.DisallowUnknownFields()
	var m nativeimage.Manifest
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF {
		f.Close()
		fmt.Fprintln(os.Stderr, nativeimage.ErrImage)
		os.Exit(1)
	}
	f.Close()
	result, e := nativeimage.Build(context.Background(), c, m)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	json.NewEncoder(os.Stdout).Encode(result)
}
