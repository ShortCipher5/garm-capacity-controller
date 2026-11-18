//go:build linux

package nativezfs

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capsulepeer"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

// CreateFresh delegates only a newly created bounded dataset tree to the actual
// paused capsule's held user namespace. It does not launch Incus, resume the
// helper, mount filesystems, admit runners or prove full capsule confinement.
// Every uncertain/partial attempt remains consumed and must never be adopted.
func CreateFresh(ctx context.Context, peer *capsulepeer.Peer, c Configuration) (Birth, error) {
	if ctx == nil || ctx.Err() != nil || os.Geteuid() != 0 || peer == nil || !peer.Paused() || !birthID(peer.BirthID()) || !validConfig(c) || securePath(c.Registry, true) != nil {
		return Birth{}, ErrRefused
	}
	st, e := os.Stat(c.Registry)
	if e != nil || st.Mode().Perm() != 0700 {
		return Birth{}, ErrRefused
	}
	ns, e := peer.UserNamespace()
	if e != nil {
		return Birth{}, ErrRefused
	}
	defer ns.Close()
	var actual, host unix.Stat_t
	kind, e := unix.IoctlRetInt(int(ns.Fd()), unix.NS_GET_NSTYPE)
	if e != nil || kind != unix.CLONE_NEWUSER || unix.Fstat(int(ns.Fd()), &actual) != nil || unix.Stat("/proc/self/ns/user", &host) != nil || actual.Ino == host.Ino || !peer.Paused() {
		return Birth{}, ErrRefused
	}
	binary, e := openBinary(c.Binary, c.BinarySHA256)
	if e != nil {
		return Birth{}, ErrRefused
	}
	defer binary.Close()
	helper, e := openBinary(c.Helper, c.HelperSHA256)
	if e != nil {
		return Birth{}, ErrRefused
	}
	defer helper.Close()
	cmd := systemCommands{binary: binary, helper: helper}
	version, e := cmd.run(ctx, []string{"version"}, ns)
	if e != nil || !supportedVersion(version) {
		return Birth{}, ErrRefused
	}
	journal := fileJournal{directory: filepath.Join(c.Registry, peer.BirthID()), id: peer.BirthID()}
	r, e := provision(ctx, c, peer.BirthID(), actual.Ino, ns, cmd, journal, peer.UID(), peer.GID(), func(r Record) error { return initializeRuntime(ctx, r, journal.directory, ns, cmd) })
	if e != nil {
		return Birth{}, e
	}
	if !peer.Paused() || ctx.Err() != nil || journal.complete(r) != nil {
		return Birth{}, ErrUnknown
	}
	// Even completion durability is not enough when the paused peer disappeared.
	if !peer.Paused() {
		return Birth{}, ErrUnknown
	}
	return Birth{record: r, config: c, peer: peer}, nil
}
func supportedVersion(raw []byte) bool { return string(raw) == "zfs-2.2.6\nzfs-kmod-2.2.6\n" }
