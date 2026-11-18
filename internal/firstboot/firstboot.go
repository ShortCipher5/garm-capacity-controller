// Package firstboot provides a separately approved full-administrator operation
// for an isolated distro OpenSSH inventory listener. It never changes global sshd.
package firstboot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

//go:embed installer.py
var installer string

const ListenerPort = 19922

var ErrInvalid = errors.New("invalid reviewed isolated-listener plan")

type Manifest struct {
	Profile         string `json:"profile"`
	Architecture    string `json:"architecture"`
	ArtifactSHA256  string `json:"artifact_sha256"`
	InstallerSHA256 string `json:"installer_sha256"`
}
type Plan struct {
	Manifest         Manifest           `json:"manifest"`
	Base             hostbootstrap.Plan `json:"base"`
	ListenAddress    string             `json:"listen_address"`
	InventoryAddress string             `json:"inventory_address"`
	InventoryPort    uint16             `json:"inventory_port"`
	EnableAtBoot     bool               `json:"enable_at_boot"`
	InstallerSHA256  string             `json:"installer_sha256"`
}
type Request struct {
	Plan         Plan   `json:"plan"`
	ReviewDigest string `json:"review_digest"`
	Artifact     []byte `json:"artifact"`
}
type Receipt struct {
	State                       string `json:"state"`
	Digest                      string `json:"review_digest"`
	ListenAddress               string `json:"listener_address"`
	ListenPort                  int    `json:"listener_port"`
	InventoryAddress            string `json:"inventory_address"`
	InventoryPort               uint16 `json:"inventory_port"`
	HostKey                     string `json:"host_key"`
	BootEnabled                 bool   `json:"boot_enabled"`
	NetworkReachabilityVerified bool   `json:"network_reachability_verified"`
}

func InstallerSHA256() string { h := sha256.Sum256([]byte(installer)); return hex.EncodeToString(h[:]) }
func Digest(p Plan) string {
	b, _ := json.Marshal(p)
	var v any
	json.Unmarshal(b, &v)
	b, _ = json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ipv4(s string) bool {
	a, e := netip.ParseAddr(s)
	return e == nil && a.Is4() && a.String() == s && a.IsGlobalUnicast() && !a.IsLoopback()
}
func Validate(r Request) error {
	p := r.Plan
	if p.Manifest.Profile != "ubuntu-24.04-systemd-nftables/v1" || p.Manifest.Architecture != p.Base.Architecture || p.Manifest.ArtifactSHA256 != p.Base.ArtifactSHA256 || p.Manifest.InstallerSHA256 != p.InstallerSHA256 {
		return ErrInvalid
	}
	if p.InstallerSHA256 != InstallerSHA256() || r.ReviewDigest != Digest(p) || !ipv4(p.ListenAddress) || !ipv4(p.InventoryAddress) || p.InventoryPort == 0 || !ipv4(p.Base.Address) || !ipv4(p.Base.ControllerIP) || p.Base.Port == ListenerPort {
		return ErrInvalid
	}
	base := hostbootstrap.Request{Plan: p.Base, ReviewDigest: hostbootstrap.Digest(p.Base), Artifact: r.Artifact}
	return hostbootstrap.Validate(base)
}

// Command contains only embedded reviewed source. No request value enters it.
func command() string {
	return "/usr/bin/sudo -n /usr/bin/python3 -I -c '" + strings.ReplaceAll(installer, "'", "'\"'\"'") + "'"
}
func Execute(ctx context.Context, r Request, key ssh.Signer) (Receipt, error) {
	return execute(ctx, r, key, (&net.Dialer{Timeout: 5 * time.Second}).DialContext)
}
func execute(ctx context.Context, r Request, key ssh.Signer, dial func(context.Context, string, string) (net.Conn, error)) (Receipt, error) {
	fail := func() (Receipt, error) {
		return Receipt{}, errors.New("first-time setup outcome unknown or refused; inspect protected phase journal and retry only the identical reviewed plan; listener may require stopping")
	}
	if Validate(r) != nil || key == nil || ssh.FingerprintSHA256(key.PublicKey()) != r.Plan.Base.AdminKeyFingerprint {
		return Receipt{}, ErrInvalid
	}
	host, e := hostbootstrap.PublicKey(r.Plan.Base.HostKey)
	if e != nil {
		return Receipt{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	address := net.JoinHostPort(r.Plan.Base.Address, strconv.Itoa(int(r.Plan.Base.Port)))
	conn, e := dial(ctx, "tcp", address)
	if e != nil {
		return fail()
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return fail()
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	cfg := &ssh.ClientConfig{User: r.Plan.Base.AdminUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyAlgorithms: []string{ssh.KeyAlgoED25519}, HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
		if subtle.ConstantTimeCompare(k.Marshal(), host.Marshal()) != 1 {
			return ErrInvalid
		}
		return nil
	}}
	c, ch, requests, e := ssh.NewClientConn(conn, address, cfg)
	if e != nil {
		return fail()
	}
	client := ssh.NewClient(c, ch, requests)
	defer client.Close()
	session, e := client.NewSession()
	if e != nil {
		return fail()
	}
	defer session.Close()
	raw, _ := json.Marshal(r)
	session.Stdin = bytes.NewReader(raw)
	out := &limited{cancel: cancel}
	stderr := &limited{cancel: cancel}
	session.Stdout = out
	session.Stderr = stderr
	if session.Run(command()) != nil || ctx.Err() != nil || out.exceeded || stderr.exceeded {
		return fail()
	}
	var receipt Receipt
	d := json.NewDecoder(bytes.NewReader(out.Bytes()))
	d.DisallowUnknownFields()
	if d.Decode(&receipt) != nil || d.Decode(new(any)) != io.EOF || ValidateReceipt(r.Plan, r.ReviewDigest, receipt) != nil {
		return fail()
	}
	return receipt, nil
}

type limited struct {
	bytes.Buffer
	cancel   context.CancelFunc
	exceeded bool
}

func (w *limited) Write(p []byte) (int, error) {
	if len(p) > 65536-w.Len() {
		w.exceeded = true
		w.cancel()
		return 0, ErrInvalid
	}
	return w.Buffer.Write(p)
}

// ValidateReceipt binds the historical result to every approved endpoint and boot choice.
func ValidateReceipt(p Plan, digest string, r Receipt) error {
	if r.State != "installed" || r.Digest != digest || r.ListenAddress != p.ListenAddress || r.ListenPort != ListenerPort || r.InventoryAddress != p.InventoryAddress || r.InventoryPort != p.InventoryPort || r.BootEnabled != p.EnableAtBoot || r.NetworkReachabilityVerified {
		return ErrInvalid
	}
	if _, e := hostbootstrap.PublicKey(r.HostKey); e != nil {
		return ErrInvalid
	}
	return nil
}
