// Package incussetup prepares a local Incus daemon on a narrowly supported OS.
// It has no admission, GARM, remote TLS trust, or placement-writing dependency.
package incussetup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed recipe.py
var recipe string
var Err = errors.New("Incus setup unavailable; inspect before retrying")
var simpleUser = regexp.MustCompile("^[a-z_][a-z0-9_-]{0,31}$")
var pkgVersion = regexp.MustCompile(`^[0-9][0-9a-zA-Z.+:~-]{0,79}$`)
var simpleVersion = regexp.MustCompile("^[0-9]+[.][0-9]+([.][0-9]+)?([-+][a-zA-Z0-9.+~-]+)?$")

type Target struct {
	Address          string `json:"address"`
	Port             uint16 `json:"port"`
	User             string `json:"admin_user"`
	HostKey          string `json:"host_key"`
	AdminFingerprint string `json:"admin_key_fingerprint"`
}
type Report struct {
	Outcome  string `json:"outcome"`
	State    string `json:"state"`
	Version  string `json:"server_version"`
	Nonce    string `json:"nonce"`
	Eligible *bool  `json:"placement_eligible"`
}

func (t Target) Valid() bool {
	_, e := hostbootstrap.PublicKey(t.HostKey)
	return e == nil && hostbootstrap.IP(t.Address) && t.Port != 0 && simpleUser.MatchString(t.User) && len(t.AdminFingerprint) == 50 && strings.HasPrefix(t.AdminFingerprint, "SHA256:")
}
func RecipeHash() string { return fmt.Sprintf("%x", sha256.Sum256([]byte(recipe))) }
func (t Target) Review(r Report) string {
	b, _ := json.Marshal([]any{"incus-setup/debian13/v1", t, RecipeHash(), r.State, r.Version, r.Nonce})
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func (r Report) Mutable() bool { return r.State == "missing" || r.State == "pristine" }
func (r Report) valid(mode, nonce string) bool {
	if r.Nonce != nonce || r.Eligible == nil || *r.Eligible || len(r.Version) > 80 || (r.State != "missing" && r.Version != "" && !simpleVersion.MatchString(r.Version)) {
		return false
	}
	switch r.State {
	case "missing":
		if !pkgVersion.MatchString(r.Version) {
			return false
		}
	case "configured", "pristine":
		if r.Version == "" {
			return false
		}
	case "manual_review", "unsupported":
	default:
		return false
	}
	if mode == "inspect" {
		return r.Outcome == "observed"
	}
	return (r.Outcome == "verified" && r.State == "configured") || r.Outcome == "changed" || r.Outcome == "refused"
}

type capBuffer struct{ bytes.Buffer }

func (b *capBuffer) Write(p []byte) (int, error) {
	if len(p) > 4096-b.Len() {
		return 0, Err
	}
	return b.Buffer.Write(p)
}
func (t Target) Run(ctx context.Context, key ssh.Signer, previous *Report) (Report, error) {
	return t.run(ctx, key, previous, (&net.Dialer{Timeout: 5 * time.Second}).DialContext)
}
func (t Target) run(ctx context.Context, key ssh.Signer, prev *Report, dial func(context.Context, string, string) (net.Conn, error)) (Report, error) {
	var z Report
	if !t.Valid() || key == nil || ssh.FingerprintSHA256(key.PublicKey()) != t.AdminFingerprint {
		return z, Err
	}
	mode, state, version := "inspect", "", ""
	if prev != nil {
		if !prev.Mutable() || !prev.valid("inspect", prev.Nonce) {
			return z, Err
		}
		mode, state, version = "apply", prev.State, prev.Version
	}
	n := make([]byte, 32)
	if _, e := rand.Read(n); e != nil {
		return z, Err
	}
	nonce := hex.EncodeToString(n)
	address := net.JoinHostPort(t.Address, strconv.Itoa(int(t.Port)))
	conn, e := dial(ctx, "tcp", address)
	if e != nil {
		return z, Err
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok || conn.SetDeadline(deadline) != nil {
		return z, Err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	host, _ := hostbootstrap.PublicKey(t.HostKey)
	c, ch, rq, e := ssh.NewClientConn(conn, address, &ssh.ClientConfig{User: t.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(host), HostKeyAlgorithms: []string{ssh.KeyAlgoED25519}})
	if e != nil {
		return z, Err
	}
	client := ssh.NewClient(c, ch, rq)
	defer client.Close()
	s, e := client.NewSession()
	if e != nil {
		return z, Err
	}
	defer s.Close()
	// No request field enters a shell command. Both script and command are compiled in.
	args, _ := json.Marshal([]string{mode, state, version, nonce})
	s.Stdin = bytes.NewReader(args)
	out := new(capBuffer)
	s.Stdout = out
	s.Stderr = io.Discard
	command := "/usr/bin/sudo -n /usr/bin/python3 -I -c '" + strings.ReplaceAll(recipe, "'", "'\"'\"'") + "'"
	if s.Run(command) != nil {
		return z, Err
	}
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	dec.DisallowUnknownFields()
	if dec.Decode(&z) != nil || dec.Decode(new(any)) != io.EOF || !z.valid(mode, nonce) {
		return Report{}, Err
	}
	return z, nil
}
