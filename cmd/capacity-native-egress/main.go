//go:build linux

package main

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeegress"
	"os"
)

func main() {
	if nativeegress.InstallHelper() != nil {
		os.Exit(1)
	}
}
