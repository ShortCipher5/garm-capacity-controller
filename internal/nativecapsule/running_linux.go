//go:build linux

package nativecapsule

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeegress"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativezfs"
	"golang.org/x/sys/unix"
)

// Running is minted only after one durable release of a real paused capsule and
// authentication of the same PID1 now executing the pinned native daemon. It is
// not proof of qualified device/network isolation, and grants no allocations.
type Running struct {
	staged        *Staged
	storage       nativezfs.Birth
	network       nativeegress.Birth
	nativeSHA     string
	transport     *http.Transport
	clientClaimed *atomic.Bool
	budgets       *storageBudgetState
}

// Finish consumes the birth before writing to its bootstrap connection. No ACK,
// timeout, process loss or partial write permits a retry or adoption.
func (s *Staged) Finish(ctx context.Context, storage nativezfs.Birth, nativeSHA, quotaHelperSHA string, network nativeegress.Birth) (*Running, error) {
	if ctx == nil || ctx.Err() != nil || s == nil || s.peer == nil || s.conn == nil || s.partition.recheck(ctx) != nil || !s.peer.Paused() || !hash(nativeSHA) || !hash(quotaHelperSHA) {
		return nil, ErrCapsule
	}
	if _, e := s.artifacts.Observe(ctx); e != nil {
		return nil, ErrCapsule
	}
	if network.Record().BirthID != s.birth.BirthID || network.Recheck(ctx) != nil {
		return nil, ErrCapsule
	}
	rec := storage.Record()
	if rec.CapsuleID != s.birth.BirthID || rec.MappedUID != s.peer.UID() || rec.MappedGID != s.peer.GID() || storage.Recheck(ctx) != nil {
		return nil, ErrCapsule
	}
	ns, e := s.peer.UserNamespace()
	if e != nil {
		return nil, ErrCapsule
	}
	var st unix.Stat_t
	e = unix.Fstat(int(ns.Fd()), &st)
	ns.Close()
	if e != nil || st.Ino != rec.UserNamespace {
		return nil, ErrCapsule
	}
	spec := bootSpec{Version: 3, Partition: s.partition.spec, ParentLimits: s.limits.Limits(), DNSResolvers: network.Policy().DNS(), BirthID: rec.CapsuleID, Dataset: rec.Dataset, DatasetGUID: rec.GUID, RuntimeGUID: rec.RuntimeGUID, PoolGUID: rec.PoolGUID, QuotaBytes: rec.QuotaBytes, ZFSSHA256: rec.BinarySHA256, QuotaHelperSHA256: quotaHelperSHA, NativeSHA256: nativeSHA}
	if !validBoot(spec) {
		return nil, ErrCapsule
	}
	raw, _ := json.Marshal(spec)
	f, e := os.OpenFile(filepath.Join(s.directory, "release.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return nil, ErrCapsule
	}
	_, e = f.Write(raw)
	se := f.Sync()
	ce := f.Close()
	if e != nil || se != nil || ce != nil || syncDir(s.directory) != nil || !s.peer.Paused() {
		return nil, ErrCapsule
	}
	deadline := time.Now().Add(90 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	s.conn.SetDeadline(deadline)
	if _, e = s.conn.Write(append(raw, '\n')); e != nil {
		return nil, ErrCapsule
	}
	ack, e := bufio.NewReaderSize(s.conn, 128).ReadString('\n')
	if e != nil || ack != "{\"version\":1,\"state\":\"native_exec_pending\"}\n" {
		return nil, ErrCapsule
	}
	r := &Running{staged: s, storage: storage, nativeSHA: nativeSHA, clientClaimed: new(atomic.Bool), network: network, budgets: &storageBudgetState{}}
	r.transport = &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: r.dial, ResponseHeaderTimeout: 30 * time.Second}
	for time.Now().Before(deadline) {
		if ctx.Err() != nil || !s.peer.Alive() {
			return nil, ErrCapsule
		}
		if !s.peer.CompleteNativePlacement(nativeSHA) {
			select {
			case <-ctx.Done():
				return nil, ErrCapsule
			case <-time.After(50 * time.Millisecond):
				continue
			}
		}
		conn, err := r.dial(ctx, "unix", "")
		if err == nil {
			conn.Close()
			if r.CheckScope(ctx) == nil {
				return r, nil
			}
			return nil, ErrCapsule
		}
		select {
		case <-ctx.Done():
			return nil, ErrCapsule
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil, ErrCapsule
}
func (r *Running) BirthID() string {
	if r == nil || r.staged == nil {
		return ""
	}
	return r.staged.birth.BirthID
}
func (r *Running) NativeSHA256() string {
	if r == nil {
		return ""
	}
	return r.nativeSHA
}
func (r *Running) LauncherSHA256() string {
	if r == nil || r.staged == nil {
		return ""
	}
	return r.staged.stageSHA
}
func (r *Running) Storage() nativezfs.Record {
	if r == nil {
		return nativezfs.Record{}
	}
	return r.storage.Record()
}
func (r *Running) ClaimClient() bool {
	return r != nil && r.clientClaimed != nil && r.staged != nil && r.staged.peer != nil && r.staged.peer.Executable(r.nativeSHA) && r.clientClaimed.CompareAndSwap(false, true)
}
func (r *Running) CheckScope(ctx context.Context) error {
	if ctx == nil || r == nil || r.staged == nil || r.staged.partition.recheck(ctx) != nil || r.staged.devices.Recheck(ctx) != nil || r.network.Recheck(ctx) != nil || !r.staged.peer.Executable(r.nativeSHA) {
		return ErrCapsule
	}
	artifact, e := r.staged.artifacts.Observe(ctx)
	if e != nil {
		return ErrCapsule
	}
	old := r.staged.limits
	now, e := nativeboundary.Observe(old.Limits())
	if e != nil || now.NamespaceInode() != old.NamespaceInode() || now.RootInode() != old.RootInode() || r.checkStorage(ctx) != nil || !r.staged.peer.Executable(r.nativeSHA) || !artifact.Fresh() {
		return ErrCapsule
	}
	return nil
}
func (r *Running) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	if r == nil || r.staged == nil || !r.staged.peer.Executable(r.nativeSHA) {
		return nil, ErrCapsule
	}
	p := r.staged.peer
	path := fmt.Sprintf("/proc/%d/root%s/incus/unix.socket", p.PID(), RuntimeRoot)
	c, e := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if e != nil {
		return nil, ErrCapsule
	}
	good := false
	defer func() {
		if !good {
			c.Close()
		}
	}()
	u, ok := c.(*net.UnixConn)
	if !ok {
		return nil, ErrCapsule
	}
	rc, e := u.SyscallConn()
	if e != nil {
		return nil, ErrCapsule
	}
	var cred *unix.Ucred
	var ce error
	e = rc.Control(func(fd uintptr) { cred, ce = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
	if e != nil || ce != nil || cred == nil || int(cred.Pid) != p.PID() || cred.Uid != p.UID() || cred.Gid != p.GID() || !p.Executable(r.nativeSHA) {
		return nil, ErrCapsule
	}
	good = true
	return c, nil
}
func (r *Running) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil || req.URL.Scheme != "http" || req.URL.Host != "native.invalid" || req.URL.User != nil || req.Host != "" && req.Host != "native.invalid" || r.CheckScope(req.Context()) != nil {
		return nil, ErrCapsule
	}
	return r.transport.RoundTrip(req)
}

func (r *Running) EgressDigest() string {
	if r == nil {
		return ""
	}
	return r.network.Policy().Digest()
}
