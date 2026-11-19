//go:build linux

package main

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativequota"
	"os"
)

func main() {
	if nativequota.ExecHelper() != nil {
		os.Exit(111)
	}
}
