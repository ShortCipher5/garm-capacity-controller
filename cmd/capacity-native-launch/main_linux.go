//go:build linux

// capacity-native-launch is a private root-owned helper, not a general command
// executor. It has no flags, shell, payload parser, configurable argv or fallback.
package main

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativelaunch"
	"os"
)

func main() {
	if nativelaunch.Exec() != nil {
		os.Exit(111)
	}
}
