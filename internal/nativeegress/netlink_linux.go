//go:build linux

package nativeegress

import (
	"encoding/binary"
	"golang.org/x/sys/unix"
	"syscall"
)

func attr(kind uint16, data []byte) []byte {
	n := 4 + len(data)
	b := make([]byte, (n+3)&^3)
	binary.NativeEndian.PutUint16(b, uint16(n))
	binary.NativeEndian.PutUint16(b[2:], kind)
	copy(b[4:], data)
	return b
}
func u32(v uint32) []byte { b := make([]byte, 4); binary.NativeEndian.PutUint32(b, v); return b }

// The peer is placed atomically into an already held namespace FD. There is no
// PID/path lookup and no intermediate host-side peer available to networkd.
func vethMessage(host string, nsfd int) []byte {
	info := make([]byte, 16)
	peer := make([]byte, 16)
	peer = append(peer, attr(unix.IFLA_IFNAME, []byte("garm0\x00"))...)
	peer = append(peer, attr(unix.IFLA_NET_NS_FD, u32(uint32(nsfd)))...)
	data := attr(1|unix.NLA_F_NESTED, peer)
	link := attr(unix.IFLA_INFO_KIND, []byte("veth\x00"))
	link = append(link, attr(unix.IFLA_INFO_DATA|unix.NLA_F_NESTED, data)...)
	info = append(info, attr(unix.IFLA_IFNAME, append([]byte(host), 0))...)
	info = append(info, attr(unix.IFLA_LINKINFO|unix.NLA_F_NESTED, link)...)
	h := make([]byte, 16)
	binary.NativeEndian.PutUint32(h, uint32(16+len(info)))
	binary.NativeEndian.PutUint16(h[4:], unix.RTM_NEWLINK)
	binary.NativeEndian.PutUint16(h[6:], unix.NLM_F_REQUEST|unix.NLM_F_ACK|unix.NLM_F_CREATE|unix.NLM_F_EXCL)
	binary.NativeEndian.PutUint32(h[8:], 1)
	return append(h, info...)
}
func createVeth(host string, nsfd int) error {
	fd, e := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if e != nil {
		return ErrUnknown
	}
	defer unix.Close(fd)
	if unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 5}) != nil || unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}) != nil {
		return ErrUnknown
	}
	if unix.Sendto(fd, vethMessage(host, nsfd), 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}) != nil {
		return ErrUnknown
	}
	b := make([]byte, 8192)
	n, source, e := unix.Recvfrom(fd, b, 0)
	if e != nil {
		return ErrUnknown
	}
	origin, ok := source.(*unix.SockaddrNetlink)
	if !ok || origin.Pid != 0 {
		return ErrUnknown
	}
	msgs, e := syscall.ParseNetlinkMessage(b[:n])
	if e != nil {
		return ErrUnknown
	}
	for _, m := range msgs {
		if m.Header.Seq == 1 && m.Header.Type == unix.NLMSG_ERROR && len(m.Data) >= 4 && int32(binary.NativeEndian.Uint32(m.Data)) == 0 {
			return nil
		}
	}
	return ErrUnknown
}
