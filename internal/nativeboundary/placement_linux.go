//go:build linux

package nativeboundary

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// PlaceOwner creates a fresh manager-delegated subgroup and moves this process
// (all its threads) into it before any native spawn. It enables CPU/memory/pids
// on the namespace root so the root remains a finite ancestor for descendants.
// No process is killed and no existing subgroup is adopted or removed on error.
// Caller MUST run Observe and verify its independent service before calling.
func PlaceOwner(o Observation) error {
	if os.Geteuid() != 0 || !o.Valid() || time.Since(o.at) < 0 || time.Since(o.at) > 5*time.Second {
		return ErrBoundary
	}
	current, e := Observe(o.limits)
	if e != nil || current.namespace != o.namespace || current.rootInode != o.rootInode {
		return ErrBoundary
	}
	raw, e := os.ReadFile("/sys/fs/cgroup/cgroup.procs")
	if e != nil || !onlyPID(string(raw), os.Getpid()) {
		return ErrBoundary
	}
	raw, e = os.ReadFile("/sys/fs/cgroup/cgroup.controllers")
	if e != nil || !controllers(string(raw)) {
		return ErrBoundary
	}
	// mkdir is the non-replay boundary for this already-created private cgroup.
	if os.Mkdir("/sys/fs/cgroup/.capacity-owner", 0700) != nil {
		return ErrBoundary
	}
	if os.WriteFile("/sys/fs/cgroup/.capacity-owner/cgroup.procs", []byte(strconv.Itoa(os.Getpid())+"\n"), 0600) != nil {
		return ErrBoundary
	}
	raw, e = os.ReadFile("/proc/self/cgroup")
	if e != nil || strings.TrimSpace(string(raw)) != "0::/.capacity-owner" {
		return ErrBoundary
	}
	raw, e = os.ReadFile("/sys/fs/cgroup/cgroup.procs")
	if e != nil || strings.TrimSpace(string(raw)) != "" {
		return ErrBoundary
	}
	if os.WriteFile("/sys/fs/cgroup/cgroup.subtree_control", []byte("+cpu +memory +pids\n"), 0600) != nil {
		return ErrBoundary
	}
	raw, e = os.ReadFile("/sys/fs/cgroup/cgroup.subtree_control")
	if e != nil || !controllers(string(raw)) {
		return ErrBoundary
	}
	after, e := Observe(o.limits)
	if e != nil || after.namespace != o.namespace || after.rootInode != o.rootInode {
		return ErrBoundary
	}
	return nil
}
func onlyPID(raw string, pid int) bool {
	fields := strings.Fields(raw)
	return pid > 0 && len(fields) == 1 && fields[0] == strconv.Itoa(pid)
}
func controllers(raw string) bool {
	m := map[string]bool{}
	for _, v := range strings.Fields(raw) {
		m[v] = true
	}
	return m["cpu"] && m["memory"] && m["pids"]
}
