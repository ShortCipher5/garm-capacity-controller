//go:build linux

package main

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecapsule"
	"os"
)

func main() {
	if nativecapsule.RunStage() != nil {
		os.Exit(111)
	}
}
