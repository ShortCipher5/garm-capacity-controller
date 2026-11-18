//go:build linux

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
	printHash := flag.Bool("print-program-sha256", false, "print fixed compiled rotation program digest without reading credentials or contacting hosts")
	planPath := flag.String("plan", "", "owner-private reviewed rotation plan")
	artifactPath := flag.String("artifact", "", "owner-private original trusted inventory artifact")
	manifestPath := flag.String("manifest", "", "owner-private original trusted deployment manifest")
	adminPath := flag.String("admin-key", "", "separate full-admin private SSH key")
	newPath := flag.String("new-inventory-key", "", "new restricted inventory private SSH key, never uploaded")
	state := flag.String("state-dir", "", "precreated owner-private durable rotation journal directory")
	approval := flag.String("approve-digest", "", "explicit approval of exact key removal and dedicated inventory listener restart")
	retry := flag.Bool("retry-unknown", false, "explicit same-plan retry after inspecting uncertain outcome")
	flag.Parse()
	if *printHash {
		fmt.Println(firstboot.RotationProgramSHA256())
		return nil
	}
	if flag.NArg() != 0 {
		return firstboot.ErrInvalid
	}
	raw, e := securefiles.Read(*planPath, 65536)
	if e != nil {
		return e
	}
	var plan firstboot.RotationPlan
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&plan) != nil || d.Decode(new(any)) != io.EOF {
		return firstboot.ErrInvalid
	}
	manifestRaw, e := securefiles.Read(*manifestPath, 4096)
	if e != nil {
		return e
	}
	var manifest firstboot.Manifest
	d = json.NewDecoder(bytes.NewReader(manifestRaw))
	d.DisallowUnknownFields()
	if d.Decode(&manifest) != nil || d.Decode(new(any)) != io.EOF || manifest != plan.Installation.Manifest {
		return firstboot.ErrInvalid
	}
	artifact, e := securefiles.Read(*artifactPath, 16<<20)
	if e != nil {
		return e
	}
	r := firstboot.RotationRequest{Plan: plan, ReviewDigest: firstboot.RotationDigest(plan), Artifact: artifact, Phase: "stage"}
	if firstboot.ValidateRotation(r) != nil {
		return firstboot.ErrInvalid
	}
	if *approval == "" {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"plan": plan, "review_digest": r.ReviewDigest, "warning": "FULL ADMIN: stage new inventory key, prove its pinned access, remove old key and stop/start only dedicated inventory listener. All its active inventory sessions terminate; runner jobs and global SSH are not targeted. Old key may remain on other services. Failures are unknown; inspect journal before explicit identical retry."})
	}
	if *approval != r.ReviewDigest {
		return firstboot.ErrInvalid
	}
	load := func(path string) (ssh.Signer, error) {
		b, e := securefiles.Read(path, 1<<20)
		if e != nil {
			return nil, e
		}
		defer func() {
			for i := range b {
				b[i] = 0
			}
		}()
		return ssh.ParsePrivateKey(b)
	}
	op, e := firstboot.JournalRotation(context.Background(), *state, r, *retry, func(ctx context.Context) (firstboot.RotationReceipt, error) {
		admin, e := load(*adminPath)
		if e != nil {
			return firstboot.RotationReceipt{}, e
		}
		next, e := load(*newPath)
		if e != nil {
			return firstboot.RotationReceipt{}, e
		}
		return firstboot.RotateInventoryKey(ctx, r, admin, next)
	})
	if e != nil {
		return fmt.Errorf("rotation outcome unknown or refused; inspect journal and remote owned state before retry")
	}
	return json.NewEncoder(os.Stdout).Encode(op)
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "inventory key rotation refused or uncertain; no rollback assumed")
		os.Exit(1)
	}
}
