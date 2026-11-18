// capacity-ssh-key creates a dedicated local inventory key. It never contacts a host.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/ssh"
)

func generate(path string, out io.Writer) error {
	if path == "" || out == nil {
		return errors.New("a private key path is required")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return errors.New("key generation failed")
	}
	defer func() {
		for i := range private {
			private[i] = 0
		}
	}()
	block, err := ssh.MarshalPrivateKey(private, "garm-capacity-inventory")
	if err != nil {
		return errors.New("key encoding failed")
	}
	data := pem.EncodeToMemory(block)
	defer func() {
		for i := range data {
			data[i] = 0
		}
	}()
	// O_EXCL prevents replacing an existing key or following a final symlink.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("cannot create private key; choose a new path in a trusted directory")
	}
	good := false
	defer func() {
		f.Close()
		if !good {
			os.Remove(path)
		}
	}()
	if err = f.Chmod(0600); err != nil {
		return errors.New("cannot protect private key")
	}
	if _, err = f.Write(data); err != nil {
		return errors.New("cannot write private key")
	}
	if err = f.Sync(); err != nil {
		return errors.New("cannot sync private key")
	}
	if err = f.Close(); err != nil {
		return errors.New("cannot close private key")
	}
	// Retain a successfully persisted key even if stdout fails; never replace it on retry.
	good = true
	pub, err := ssh.NewPublicKey(public)
	if err != nil {
		return errors.New("cannot encode public key")
	}
	if _, err = out.Write(ssh.MarshalAuthorizedKey(pub)); err != nil {
		return errors.New("private key saved, but public key output failed; derive it from the private key")
	}
	return nil
}
func main() {
	path := flag.String("private-key", "", "new private-key path in a trusted directory (never overwritten)")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected argument")
		os.Exit(2)
	}
	if err := generate(*path, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
