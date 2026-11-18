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
	// Success is returned only after durable local fence and a halt request.
	if err == nil {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "protected runner lifecycle unavailable; retain unknown state")
		if os.Getpid() != 1 {
			os.Exit(1)
		}
	}
	// Failure never authorizes guest shutdown or physical capacity release.
	for {
		time.Sleep(time.Hour)
	}
}
