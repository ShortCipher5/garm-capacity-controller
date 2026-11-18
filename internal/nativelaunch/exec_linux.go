//go:build linux

// Package nativelaunch implements the fixed child-side descriptor boundary.
package nativelaunch

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

var ErrLaunch = errors.New("native launcher refused")

// Exec runs only in the dedicated helper process, never in the controller.
// The parent supplies an already opened root-owned incusd at descriptor 3.
// There are no selectable arguments or fallback executable paths.
func Exec() error {
	if os.Geteuid() != 0 || len(os.Args) != 1 {
		return ErrLaunch
	}
	var st unix.Stat_t
	if unix.Fstat(3, &st) != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0022 != 0 || st.Mode&0111 == 0 {
		return ErrLaunch
	}
	return sanitizeAndExec()
}
func sanitizeAndExec() error {
	// This occurs in the CHILD just before exec. A parent-side descriptor scan
	// races threads and exec.Command alone preserves ambient non-CLOEXEC fds.
	// CLOEXEC preserves this helper's runtime descriptors until the exec syscall.
	// Only stdio and the parent's pinned regular executable descriptor survive.
	if err := unix.CloseRange(4, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return ErrLaunch
	}
	return syscall.Exec("/proc/self/fd/3", []string{"incusd"}, os.Environ())
}
