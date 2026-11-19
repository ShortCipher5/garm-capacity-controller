//go:build linux

package nativeegress

import (
	"bytes"
	"context"
	"encoding/json"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"time"
)

// Recheck reads actual host firewall and both veth ends in the held namespace.
// No adopted JSON record or caller boolean can mint this capability.
func (b Birth) Recheck(ctx context.Context) (result error) {
	if b.failed == nil || b.failed.Load() {
		return ErrRefused
	}
	defer func() {
		if result != nil {
			b.failed.Store(true)
		}
	}()
	if ctx == nil || ctx.Err() != nil || b.peer == nil || !b.peer.Alive() || b.record.BirthID != b.peer.BirthID() || b.policy.digest != b.record.PolicyDigest {
		return ErrRefused
	}
	ns, e := b.peer.NetworkNamespace()
	if e != nil {
		return ErrRefused
	}
	defer ns.Close()
	var st unix.Stat_t
	if unix.Fstat(int(ns.Fd()), &st) != nil || st.Ino != b.record.Namespace {
		return ErrRefused
	}
	ip, e := openBinary(b.config.IP, b.record.IPSHA256)
	if e != nil {
		return e
	}
	defer ip.Close()
	nft, e := openBinary(b.config.NFT, b.record.NFTSHA256)
	if e != nil {
		return e
	}
	defer nft.Close()
	helper, e := openBinary(b.config.Helper, b.record.HelperSHA256)
	if e != nil {
		return e
	}
	defer helper.Close()
	raw, _ := json.Marshal(installRequest{1, b.record.BirthID, b.record.Namespace, b.policy.spec})
	child, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(child, "/proc/self/fd/6", "inspect")
	cmd.ExtraFiles = []*os.File{ns, ip, nft, helper}
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	cmd.Dir = "/"
	cmd.Stdin = bytes.NewReader(raw)
	var output boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if cmd.Run() != nil || !b.peer.Alive() {
		return ErrUnknown
	}
	var observed observation
	if json.Unmarshal(output.Bytes(), &observed) != nil || observed != b.observed {
		return ErrUnknown
	}
	return nil
}
