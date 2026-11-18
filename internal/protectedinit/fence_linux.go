//go:build linux

package protectedinit

import (
	"golang.org/x/sys/unix"
	"net"
	"os"
)

// closeGuestNetwork runs only after PID1 has reaped every descendant. It does
// not cancel a running job. It closes the fixed image's sole external interface,
// then rereads kernel state. This is local network closure, not proof of host
// containment, GitHub completion, credential revocation or physical deletion.
func closeGuestNetwork() error {
	if os.Getpid() != 1 || os.Geteuid() != 0 {
		return ErrRefused
	}
	fd, e := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if e != nil {
		return ErrRefused
	}
	defer unix.Close(fd)
	return closeGuestNetworkWith(net.Interfaces, func(name string) error {
		req, e := unix.NewIfreq(name)
		if e != nil {
			return e
		}
		if e = unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, req); e != nil {
			return e
		}
		req.SetUint16(req.Uint16() &^ unix.IFF_UP)
		return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, req)
	})
}

func closeGuestNetworkWith(read func() ([]net.Interface, error), down func(string) error) error {
	before, e := read()
	if e != nil {
		return ErrRefused
	}
	identity, ok := guestNetworkIdentity(before)
	if !ok {
		return ErrRefused
	}
	if e = down("eth0"); e != nil {
		return ErrRefused
	}
	after, e := read()
	if e != nil {
		return ErrRefused
	}
	next, ok := guestNetworkIdentity(after)
	if !ok || next != identity {
		return ErrRefused
	}
	for _, i := range after {
		if i.Name == "eth0" && i.Flags&net.FlagUp != 0 {
			return ErrRefused
		}
	}
	return nil
}
func guestNetworkIdentity(all []net.Interface) (int, bool) {
	if len(all) != 2 {
		return 0, false
	}
	seenLoop, idx := false, 0
	for _, i := range all {
		switch i.Name {
		case "lo":
			if seenLoop || i.Flags&net.FlagLoopback == 0 {
				return 0, false
			}
			seenLoop = true
		case "eth0":
			if idx != 0 || i.Index <= 0 || i.Flags&net.FlagLoopback != 0 {
				return 0, false
			}
			idx = i.Index
		default:
			return 0, false
		}
	}
	return idx, seenLoop && idx > 0
}
