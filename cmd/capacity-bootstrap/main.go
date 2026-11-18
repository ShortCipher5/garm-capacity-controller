//go:build linux

// capacity-bootstrap reviews or explicitly executes a separate privileged install
// on a prepared host. The inventory gateway never receives this credential.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"golang.org/x/crypto/ssh"
	"io"
	"os"
)

func run() error {
	planPath := flag.String("plan", "", "JSON plan path (no private key)")
	artifactPath := flag.String("artifact", "", "trusted static Linux inventory binary")
	adminPath := flag.String("admin-key", "", "separate owner-private administrator key file")
	approved := flag.String("approve-digest", "", "execute only this exact previously reviewed plan digest")
	flag.Parse()
	if flag.NArg() != 0 {
		return hostbootstrap.ErrInvalid
	}
	raw, e := os.Open(*planPath)
	if e != nil {
		return e
	}
	defer raw.Close()
	d := json.NewDecoder(io.LimitReader(raw, 65537))
	d.DisallowUnknownFields()
	var p hostbootstrap.Plan
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		return hostbootstrap.ErrInvalid
	}
	f, e := os.Open(*artifactPath)
	if e != nil {
		return e
	}
	defer f.Close()
	artifact, e := io.ReadAll(io.LimitReader(f, hostbootstrap.MaxArtifact+1))
	if e != nil {
		return e
	}
	key, e := securefiles.Read(*adminPath, 1<<20)
	if e != nil {
		return fmt.Errorf("administrator key unavailable")
	}
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()
	signer, e := ssh.ParsePrivateKey(key)
	if e != nil {
		return fmt.Errorf("administrator key invalid")
	}
	fingerprint := ssh.FingerprintSHA256(signer.PublicKey())
	hash := sha256.Sum256(artifact)
	// Never silently repair a previously reviewed identity or artifact.
	if p.AdminKeyFingerprint == "" {
		p.AdminKeyFingerprint = fingerprint
	}
	if p.ArtifactSHA256 == "" {
		p.ArtifactSHA256 = hex.EncodeToString(hash[:])
	}
	r := hostbootstrap.Request{Plan: p, Artifact: artifact, ReviewDigest: hostbootstrap.Digest(p)}
	if e = hostbootstrap.Validate(r); e != nil {
		return e
	}
	if fingerprint != p.AdminKeyFingerprint {
		return hostbootstrap.ErrInvalid
	}
	if *approved == "" {
		return json.NewEncoder(os.Stdout).Encode(struct {
			Plan   hostbootstrap.Plan `json:"plan"`
			Digest string             `json:"review_digest"`
			Action string             `json:"action"`
		}{p, r.ReviewDigest, "Install fixed inventory binary and one restricted key on a prepared host; no account creation or sshd changes"})
	}
	if *approved != r.ReviewDigest {
		return fmt.Errorf("review changed; review again before installation")
	}
	if e = hostbootstrap.Execute(context.Background(), r, signer); e != nil {
		return e
	}
	fmt.Println("Installation recorded. Separately register and authorize a restricted inventory probe; no capacity enrollment occurred.")
	return nil
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
