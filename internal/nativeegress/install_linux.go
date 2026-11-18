//go:build linux

package nativeegress

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capsulepeer"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"
)

type Configuration struct{ IP, IPSHA256, NFT, NFTSHA256, Helper, HelperSHA256, Registry string }
type Record struct {
	TransitCIDR  string `json:"transit_cidr"`
	Version      int    `json:"version"`
	BirthID      string `json:"birth_id"`
	Namespace    uint64 `json:"namespace"`
	PolicyDigest string `json:"policy_digest"`
	IPSHA256     string `json:"ip_sha256"`
	NFTSHA256    string `json:"nft_sha256"`
	HelperSHA256 string `json:"helper_sha256"`
}
type Birth struct {
	failed   *atomic.Bool
	config   Configuration
	observed observation
	record   Record
	policy   Policy
	peer     *capsulepeer.Peer
}

func (b Birth) Record() Record { return b.record }
func (b Birth) Policy() Policy { return b.policy }

// CreateFresh is the only constructor. It requires the authenticated paused
// capsule, records intent durably, then invokes bounded fixed operations in a
// separate pinned privileged process. No interface/table is adopted or deleted.
func CreateFresh(ctx context.Context, peer *capsulepeer.Peer, p Policy, c Configuration) (Birth, error) {
	if ctx == nil || ctx.Err() != nil || os.Geteuid() != 0 || peer == nil || !peer.Paused() || !birth(peer.BirthID()) || p.digest == "" || securePath(c.Registry, true) != nil {
		return Birth{}, ErrRefused
	}
	st, e := os.Stat(c.Registry)
	if e != nil || st.Mode().Perm() != 0700 {
		return Birth{}, ErrRefused
	}
	ns, e := peer.NetworkNamespace()
	if e != nil {
		return Birth{}, ErrRefused
	}
	defer ns.Close()
	var s, h unix.Stat_t
	kind, e := unix.IoctlRetInt(int(ns.Fd()), unix.NS_GET_NSTYPE)
	if e != nil || kind != unix.CLONE_NEWNET || unix.Fstat(int(ns.Fd()), &s) != nil || unix.Stat("/proc/self/ns/net", &h) != nil || s.Ino == h.Ino {
		return Birth{}, ErrRefused
	}
	ip, e := openBinary(c.IP, c.IPSHA256)
	if e != nil {
		return Birth{}, e
	}
	defer ip.Close()
	nft, e := openBinary(c.NFT, c.NFTSHA256)
	if e != nil {
		return Birth{}, e
	}
	defer nft.Close()
	helper, e := openBinary(c.Helper, c.HelperSHA256)
	if e != nil {
		return Birth{}, e
	}
	defer helper.Close()
	r := Record{p.spec.TransitCIDR, 1, peer.BirthID(), s.Ino, p.digest, c.IPSHA256, c.NFTSHA256, c.HelperSHA256}
	dir := filepath.Join(c.Registry, r.BirthID)
	if reserveNetwork(c.Registry, r) != nil {
		return Birth{}, ErrUnknown
	}
	if !peer.Paused() {
		return Birth{}, ErrUnknown
	}
	request := installRequest{1, r.BirthID, r.Namespace, p.spec}
	raw, _ := json.Marshal(request)
	child, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(child, "/proc/self/fd/6")
	cmd.ExtraFiles = []*os.File{ns, ip, nft, helper}
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	cmd.Dir = "/"
	cmd.Stdin = bytes.NewReader(raw)
	var output boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	if cmd.Run() != nil || ctx.Err() != nil || !peer.Paused() {
		return Birth{}, ErrUnknown
	}
	var observed observation
	if json.Unmarshal(output.Bytes(), &observed) != nil || observed.Host.Index <= 0 || observed.Guest.Index <= 0 || writeRecord(dir, "complete.json", r) != nil || !peer.Paused() {
		return Birth{}, ErrUnknown
	}
	return Birth{record: r, policy: p, peer: peer, config: c, observed: observed, failed: new(atomic.Bool)}, nil
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func writeRecord(dir, name string, r Record) error {
	raw, _ := json.Marshal(r)
	f, e := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	n, e := f.Write(raw)
	se := f.Sync()
	ce := f.Close()
	if e != nil || n != len(raw) || se != nil || ce != nil {
		return ErrUnknown
	}
	return syncDir(dir)
}
