package managedgarm

import (
	"net"
	"strconv"
	"strings"
	"syscall"
)

// Held identity only: not exclusive custody, namespace admission, or authority.
// This primitive never opens a namespace, creates a listener, or serves requests.
type stockReadListener struct {
	listener *net.TCPListener
	inode    uint64
}

func stockReadListenerInode(listener *net.TCPListener) (uint64, error) {
	if listener == nil {
		return 0, ErrBoundary
	}
	raw, err := listener.SyscallConn()
	if err != nil {
		return 0, ErrBoundary
	}
	var inode uint64
	err = raw.Control(func(fd uintptr) {
		var st syscall.Stat_t
		if syscall.Fstat(int(fd), &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFSOCK || st.Ino == 0 {
			return
		}
		addr, e := syscall.Getsockname(int(fd))
		a, ok := addr.(*syscall.SockaddrInet4)
		if e != nil || !ok || a.Port != 9445 || a.Addr != [4]byte{127, 0, 0, 1} {
			return
		}
		accepting, e := syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_ACCEPTCONN)
		if e != nil || accepting != 1 {
			return
		}
		// Linux SO_REUSEPORT: an independently accepting socket must not share this port.
		reuse, e := syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, 15)
		if e != nil || reuse != 0 {
			return
		}
		inode = st.Ino
	})
	if err != nil || inode == 0 {
		return 0, ErrBoundary
	}
	return inode, nil
}

func retainStockReadListener(listener *net.TCPListener) (*stockReadListener, error) {
	inode, err := stockReadListenerInode(listener)
	if err != nil {
		return nil, err
	}
	return &stockReadListener{listener: listener, inode: inode}, nil
}

// A nil identity means exact preboot four ports. Supervised five ports requires
// the same still-open accepting FD before and after table comparison. The caller
// must eventually obtain the table from a sealed lifecycle's verified namespace;
// supplied bytes alone cannot establish that provenance. Observe remains preboot.
func stockReadListenerSet(tables [2][]byte, held *stockReadListener) bool {
	for _, table := range tables {
		if len(table) > 4<<20 {
			return false
		}
	}
	if held == nil {
		return stockReadPrebootListeners(tables)
	}
	before, err := stockReadListenerInode(held.listener)
	if err != nil || before != held.inode {
		return false
	}
	filtered := tables
	lines := strings.Split(string(tables[0]), "\n")
	if len(lines) == 0 {
		return false
	}
	out := []string{lines[0]}
	found := false
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) >= 4 && f[3] == "0A" && f[1] == "0100007F:24E5" {
			if found || len(f) < 10 {
				return false
			}
			inode, e := strconv.ParseUint(f[9], 10, 64)
			if e != nil || inode == 0 || inode != before {
				return false
			}
			found = true
			continue
		}
		out = append(out, line)
	}
	filtered[0] = []byte(strings.Join(out, "\n"))
	if !found || !stockReadPrebootListeners(filtered) {
		return false
	}
	after, err := stockReadListenerInode(held.listener)
	return err == nil && after == before
}
