//go:build linux

package main

import (
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedinit"
	"os"
	"time"
)

func main() {
	var e error
	if len(os.Args) == 2 && os.Args[1] == "--runner-child" {
		e = protectedinit.ChildRunner()
	} else if len(os.Args) == 1 {
		e = protectedinit.Boot()
	} else {
		e = protectedinit.ErrRefused
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, "protected boot unavailable; retain unknown state")
		if os.Getpid() != 1 {
			os.Exit(1)
		}
	}
	for {
		time.Sleep(time.Hour)
	}
}
