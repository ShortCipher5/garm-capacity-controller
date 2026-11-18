// capacity-inventory-shell is a login-shell replacement, never a command shell.
// Even an alternate SSH authorization path cannot turn this account into a shell.
package main

import (
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
	"io"
	"os"
	"time"
)

func run(args []string, original string, in io.Reader, out io.Writer) error {
	if len(args) != 2 || args[0] != "-c" || args[1] != "garm-inventory-v1" || original != "/bin/sh -s" {
		return fmt.Errorf("unsupported inventory invocation")
	}
	n, e := io.Copy(io.Discard, io.LimitReader(in, 8193))
	if e != nil || n > 8192 {
		return fmt.Errorf("invalid inventory input")
	}
	return hostinventory.Write(out, hostinventory.Collect())
}
func main() {
	timer := time.AfterFunc(10*time.Second, func() { os.Exit(1) })
	defer timer.Stop()
	if run(os.Args[1:], os.Getenv("SSH_ORIGINAL_COMMAND"), os.Stdin, os.Stdout) != nil {
		fmt.Fprintln(os.Stderr, "inventory unavailable")
		os.Exit(1)
	}
}
