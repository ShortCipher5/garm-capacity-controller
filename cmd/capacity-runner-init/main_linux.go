//go:build linux

package main

import (
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedinit"
	"os"
	"time"
)

func main() {
	var err error
	if len(os.Args) == 2 && os.Args[1] == "--child" {
		err = protectedinit.Child()
	} else if len(os.Args) == 2 {
		var c protectedinit.Config
		c, err = protectedinit.Load(os.Args[1])
		if err == nil {
			err = protectedinit.Run(c)
		}
	} else {
		err = protectedinit.ErrRefused
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "protected runner lifecycle unavailable; retain unknown state")
		if os.Getpid() != 1 {
			os.Exit(1)
		}
	}
	// PID1 must remain alive after process exit: exiting could terminate namespace
	// activity not established by this component. No successful guest shutdown.
	for {
		time.Sleep(time.Hour)
	}
}
