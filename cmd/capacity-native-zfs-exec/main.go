//go:build linux

package main

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativezfs"
	"os"
)

func main() {
	if nativezfs.ExecHelper() != nil {
		os.Exit(1)
	}
}
