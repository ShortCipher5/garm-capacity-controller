//go:build linux

package nativezfs

import (
	"context"
	"golang.org/x/sys/unix"
	"os"
)

// Recheck observes actual host-side dataset identities and immutable parent quota
// after the pinned helper has become Incus. It does not require the process to
// remain paused. The containing Running handle must separately recheck native
// executable/namespace/cgroup ownership. No record-only constructor exists.
func (b Birth) Recheck(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil || os.Geteuid() != 0 || b.peer == nil || !b.peer.Alive() || b.peer.BirthID() != b.record.CapsuleID || !validConfig(b.config) {
		return ErrRefused
	}
	ns, e := b.peer.UserNamespace()
	if e != nil {
		return ErrRefused
	}
	defer ns.Close()
	var st unix.Stat_t
	if unix.Fstat(int(ns.Fd()), &st) != nil || st.Ino != b.record.UserNamespace {
		return ErrRefused
	}
	binary, e := openBinary(b.config.Binary, b.config.BinarySHA256)
	if e != nil {
		return ErrRefused
	}
	defer binary.Close()
	helper, e := openBinary(b.config.Helper, b.config.HelperSHA256)
	if e != nil {
		return ErrRefused
	}
	defer helper.Close()
	if checkBirth(ctx, b.record, ns, systemCommands{binary, helper}) != nil || !b.peer.Alive() {
		return ErrUnknown
	}
	return nil
}
func checkBirth(ctx context.Context, r Record, ns *os.File, cmd commands) error {
	root, e := read(ctx, cmd, r.RootDataset, ns)
	if e != nil || root["guid"] != r.RootGUID || root["type"] != "filesystem" || root["zoned"] != "off" {
		return ErrUnknown
	}
	parent, e := read(ctx, cmd, r.Dataset, ns)
	if e != nil || parent["guid"] != r.GUID || !parentProperties(parent, r.QuotaBytes, "on") {
		return ErrUnknown
	}
	for _, child := range []struct{ name, guid string }{{"runtime", r.RuntimeGUID}, {"pool", r.PoolGUID}} {
		p, e := read(ctx, cmd, r.Dataset+"/"+child.name, ns)
		if e != nil || p["guid"] != child.guid || p["type"] != "filesystem" || p["quota"] != "0" || p["zoned"] != "on" {
			return ErrUnknown
		}
	}
	parent, e = read(ctx, cmd, r.Dataset, ns)
	if e != nil || parent["guid"] != r.GUID || !parentProperties(parent, r.QuotaBytes, "on") {
		return ErrUnknown
	}
	return nil
}
