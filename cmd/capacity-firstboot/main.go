//go:build linux

// capacity-firstboot reviews and explicitly provisions an isolated inventory SSH
// listener using a separate full-admin credential, never ordinary inventory keys.
package main

import (
	"bytes"
	"context"

	"encoding/json"
	"flag"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/firstboot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"golang.org/x/crypto/ssh"
	"io"
	"os"
)

func run() error {
	manifestPath := flag.String("manifest", "", "trusted owner-private deployment manifest; not a browser-supplied checksum")
	planPath := flag.String("plan", "", "reviewed isolated-listener JSON plan")
	artifactPath := flag.String("artifact", "", "trusted static capacity-inventory-shell artifact")
	keyPath := flag.String("admin-key", "", "separate full-admin owner-private SSH key")
	approval := flag.String("approve-digest", "", "explicitly approve exact plan and fixed installer digest")
	flag.Parse()
	if flag.NArg() != 0 {
		return firstboot.ErrInvalid
	}
	f, e := os.Open(*planPath)
	if e != nil {
		return e
	}
	defer f.Close()
	var p firstboot.Plan
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		return firstboot.ErrInvalid
	}
	f2, e := os.Open(*artifactPath)
	if e != nil {
		return e
	}
	defer f2.Close()
	artifact, e := io.ReadAll(io.LimitReader(f2, (16<<20)+1))
	if e != nil {
		return e
	}
	raw, e := securefiles.Read(*keyPath, 1<<20)
	if e != nil {
		return e
	}
	defer func() {
		for i := range raw {
			raw[i] = 0
		}
	}()
	key, e := ssh.ParsePrivateKey(raw)
	if e != nil {
		return fmt.Errorf("administrator key invalid")
	}
	manifestBytes, e := securefiles.Read(*manifestPath, 4096)
	if e != nil {
		return fmt.Errorf("trusted deployment manifest unavailable")
	}
	var manifest firstboot.Manifest
	md := json.NewDecoder(bytes.NewReader(manifestBytes))
	md.DisallowUnknownFields()
	if md.Decode(&manifest) != nil || md.Decode(new(any)) != io.EOF {
		return firstboot.ErrInvalid
	}
	if p.Manifest != (firstboot.Manifest{}) && p.Manifest != manifest {
		return firstboot.ErrInvalid
	}
	p.Manifest = manifest
	if p.Base.ArtifactSHA256 == "" {
		p.Base.ArtifactSHA256 = manifest.ArtifactSHA256
	}
	if p.Base.AdminKeyFingerprint == "" {
		p.Base.AdminKeyFingerprint = ssh.FingerprintSHA256(key.PublicKey())
	}
	if p.InstallerSHA256 == "" {
		p.InstallerSHA256 = manifest.InstallerSHA256
	}
	r := firstboot.Request{Plan: p, ReviewDigest: firstboot.Digest(p), Artifact: artifact}
	if firstboot.Validate(r) != nil || p.Base.AdminKeyFingerprint != ssh.FingerprintSHA256(key.PublicKey()) {
		return firstboot.ErrInvalid
	}
	if *approval == "" {
		return json.NewEncoder(os.Stdout).Encode(struct {
			Plan    firstboot.Plan `json:"plan"`
			Digest  string         `json:"review_digest"`
			Warning string         `json:"warning"`
		}{p, r.ReviewDigest, "FULL ADMIN: create dedicated account, isolated OpenSSH service/host key and source-drop nftables table; bind local TCP19922. Existing firewall may block reachability. No global sshd changes. Unknown failures require protected-journal inspection."})
	}
	if *approval != r.ReviewDigest {
		return fmt.Errorf("review changed; explicit fresh approval required")
	}
	receipt, e := firstboot.Execute(context.Background(), r, key)
	if e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(receipt)
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
