// capacity-host-inventory is an OpenSSH forced command, not a general shell.
package main

import (
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
	"io"
	"os"
	"time"
)

func run(args []string, original string, in io.Reader, out io.Writer) error {
	if len(args) != 0 || original != "/bin/sh -s" {
		return fmt.Errorf("unsupported inventory request")
	}
	// Drain bounded stdin so the existing SSH client's copy completes before exit.
	// Input is discarded, never interpreted or executed. main enforces a hard time bound.
	n, e := io.Copy(io.Discard, io.LimitReader(in, 8193))
	if e != nil || n > 8192 {
		return fmt.Errorf("unsupported inventory input")
	}
	return hostinventory.Write(out, hostinventory.Collect())
}
func main() {
	// Also bounds a client withholding EOF or an output peer that stops reading.
	timer := time.AfterFunc(10*time.Second, func() { os.Exit(1) })
	defer timer.Stop()
	if e := run(os.Args[1:], os.Getenv("SSH_ORIGINAL_COMMAND"), os.Stdin, os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, "inventory unavailable")
		os.Exit(1)
	}
}
