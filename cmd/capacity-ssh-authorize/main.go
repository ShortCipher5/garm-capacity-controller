// capacity-ssh-authorize prints a restricted authorized_keys entry. It changes no files.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"golang.org/x/crypto/ssh"
	"io"
	"net/netip"
	"os"
)

func entry(in io.Reader, out io.Writer, source string) error {
	ip, e := netip.ParseAddr(source)
	if e != nil || ip.Is4In6() || ip.Zone() != "" || ip.String() != source || !ip.IsGlobalUnicast() {
		return fmt.Errorf("an explicit controller source IP is required")
	}
	data, e := io.ReadAll(io.LimitReader(in, 16385))
	if e != nil || len(data) > 16384 {
		return fmt.Errorf("public key unavailable")
	}
	key, _, opts, rest, e := ssh.ParseAuthorizedKey(data)
	if e != nil || len(opts) != 0 || len(bytes.TrimSpace(rest)) != 0 || key.Type() != ssh.KeyAlgoED25519 {
		return fmt.Errorf("one plain Ed25519 public key is required")
	}
	_, e = fmt.Fprintf(out, "restrict,from=\"%s\",command=\"/usr/local/libexec/capacity-host-inventory\" %s", ip.String(), ssh.MarshalAuthorizedKey(key))
	return e
}
func main() {
	source := flag.String("controller-ip", "", "exact controller egress IP allowed to use this key")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(2)
	}
	if e := entry(os.Stdin, os.Stdout, *source); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
