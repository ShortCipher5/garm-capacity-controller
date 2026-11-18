//go:build linux

package capsulepeer

import (
	"golang.org/x/sys/unix"
	"os"
)

// PIDNamespace duplicates the original held namespace, never a current PID path.
func (p *Peer) PIDNamespace() (*os.File, error) {
	if p == nil {
		return nil, ErrPeer
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fd, ok := p.fds["pid"]
	if !ok || !p.aliveLocked() {
		return nil, ErrPeer
	}
	dup, e := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if e != nil {
		return nil, ErrPeer
	}
	return os.NewFile(uintptr(dup), "held-capsule-pidns"), nil
}
