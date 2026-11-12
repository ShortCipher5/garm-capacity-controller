//go:build linux

// Package supervisorsender observes one Linux Unix-packet sender. It does not
// enroll actors, authenticate their code, authorize execution or consume history.
package supervisorsender

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const MaxPayload = 4096
const maxWait = 5 * time.Second

var (
	ErrEndpoint    = errors.New("supervisor sender: unavailable Unix packet endpoint")
	ErrObservation = errors.New("supervisor sender: unavailable sender observation")
)

// Observation owns only a kernel-delivered pidfd and copied packet data. Copies
// share Close state. No raw pidfd, signaling, process-code authentication,
// enrollment, matching-to-an-existing-launch or action authorization is exposed.
type Observation struct{ state *observationState }
type observationState struct {
	mu     sync.Mutex
	fd     int
	sender Sender
	data   []byte
}

// Sender is copied kernel ancillary metadata, not a claimed process role. A
// privileged sender may supply permitted SCM_CREDENTIALS; hostile host authority
// is outside this mechanism. A queued packet can outlive its sending process.
type Sender struct {
	PID int32
	UID uint32
	GID uint32
}

// Snapshot returns copied data while the observation retains its kernel pidfd.
// It does not establish liveness, immutable code, peer enrollment, exclusive UID,
// current connection-FD holder, durable history, launch, drain or release proof.
func (o Observation) Snapshot() (Sender, []byte, bool) {
	if o.state == nil {
		return Sender{}, nil, false
	}
	o.state.mu.Lock()
	defer o.state.mu.Unlock()
	if o.state.fd < 0 {
		return Sender{}, nil, false
	}
	return o.state.sender, append([]byte(nil), o.state.data...), true
}

func (o Observation) Close() error {
	if o.state == nil {
		return nil
	}
	o.state.mu.Lock()
	defer o.state.mu.Unlock()
	if o.state.fd < 0 {
		return nil
	}
	fd := o.state.fd
	o.state.fd = -1
	o.state.data = nil
	if unix.Close(fd) != nil {
		return ErrObservation
	}
	return nil
}

// Observe consumes at most one packet from an exclusively caller-owned,
// already-connected AF_UNIX SOCK_SEQPACKET endpoint. It duplicates the descriptor
// and enables SO_PASSCRED and SO_PASSPIDFD on this socket; callers must not share
// its queue/options with another consumer. Unsupported/denied kernel facilities
// fail closed, with no stream, SO_PEERCRED-only or pidfd_open fallback.
//
// The caller must enable these receiver options BEFORE the sender transmits;
// PrepareEndpoint can do so. Observe enables/checks again but cannot retrofit a
// queued packet's missing sender identity. Failure consumes no action authority
// and must not be converted into an execution retry. Poll/receive waiting uses a
// soft five-second/context bound, not a hard kernel/scheduler deadline.
func Observe(ctx context.Context, endpoint *os.File, maximum int) (Observation, error) {
	return observe(ctx, endpoint, maximum, unix.CmsgSpace(unix.SizeofUcred)+unix.CmsgSpace(4)+unix.CmsgSpace(253*4))
}

// PrepareEndpoint enables packet credential collection on the caller-owned
// endpoint before traffic. Only this socket's receive options change; no listener,
// filesystem endpoint, namespace, policy, privilege or process is created.
func PrepareEndpoint(endpoint *os.File) error {
	fd, err := duplicate(endpoint)
	if err != nil {
		return ErrEndpoint
	}
	defer unix.Close(fd)
	return prepare(fd)
}

func duplicate(file *os.File) (int, error) {
	if file == nil {
		return -1, ErrEndpoint
	}
	raw, err := file.SyscallConn()
	if err != nil {
		return -1, ErrEndpoint
	}
	fd := -1
	var inner error
	err = raw.Control(func(original uintptr) { fd, inner = unix.FcntlInt(original, unix.F_DUPFD_CLOEXEC, 0) })
	if err != nil || inner != nil || fd < 0 {
		if fd >= 0 {
			unix.Close(fd)
		}
		return -1, ErrEndpoint
	}
	return fd, nil
}

func prepare(fd int) error {
	kind, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || kind != unix.SOCK_SEQPACKET {
		return ErrEndpoint
	}
	address, err := unix.Getsockname(fd)
	if err != nil {
		return ErrEndpoint
	}
	if _, ok := address.(*unix.SockaddrUnix); !ok {
		return ErrEndpoint
	}
	peer, err := unix.Getpeername(fd)
	if err != nil {
		return ErrEndpoint
	}
	if _, ok := peer.(*unix.SockaddrUnix); !ok {
		return ErrEndpoint
	}
	if err = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		return ErrEndpoint
	}
	if err = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PASSPIDFD, 1); err != nil {
		return ErrEndpoint
	}
	for _, option := range []int{unix.SO_PASSCRED, unix.SO_PASSPIDFD} {
		value, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, option)
		if err != nil || value != 1 {
			return ErrEndpoint
		}
	}
	return nil
}

func observe(ctx context.Context, endpoint *os.File, maximum, ancillaryBytes int) (Observation, error) {
	if ctx == nil || maximum <= 0 || maximum > MaxPayload {
		return Observation{}, ErrObservation
	}
	if ctx.Err() != nil {
		return Observation{}, ErrObservation
	}
	fd, err := duplicate(endpoint)
	if err != nil {
		return Observation{}, ErrEndpoint
	}
	defer unix.Close(fd)
	if err = prepare(fd); err != nil {
		return Observation{}, ErrEndpoint
	}
	deadline := time.Now().Add(maxWait)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	data := make([]byte, maximum+1)
	oob := make([]byte, ancillaryBytes)
	for {
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return Observation{}, ErrObservation
		}
		wait := time.Until(deadline)
		if wait > 25*time.Millisecond {
			wait = 25 * time.Millisecond
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		_, err = unix.Poll(poll, int((wait+time.Millisecond-1)/time.Millisecond))
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return Observation{}, ErrObservation
		}
		if poll[0].Revents == 0 {
			continue
		}
		n, on, flags, _, err := unix.Recvmsg(fd, data, oob, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC|unix.MSG_TRUNC)
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK || err == unix.EINTR {
			continue
		}
		if err != nil {
			return Observation{}, ErrObservation
		}
		sender, pidfd, ancillaryOK := parseAncillary(oob[:on])
		// parseAncillary closes all delivered descriptors except this one valid pidfd.
		if !ancillaryOK {
			return Observation{}, ErrObservation
		}
		if n <= 0 || n > maximum || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || ctx.Err() != nil || !time.Now().Before(deadline) {
			unix.Close(pidfd)
			return Observation{}, ErrObservation
		}
		return Observation{state: &observationState{fd: pidfd, sender: sender, data: append([]byte(nil), data[:n]...)}}, nil
	}
}

func parseAncillary(oob []byte) (Sender, int, bool) {
	var sender Sender
	descriptors := make(map[int]bool)
	pidfd := -1
	credentials := 0
	pidfds := 0
	valid := true
	for len(oob) > 0 {
		if len(oob) < unix.CmsgLen(0) {
			valid = false
			break
		}
		header, data, rest, err := unix.ParseOneSocketControlMessage(oob)
		if err != nil {
			valid = false
			break
		}
		oob = rest
		if header.Level != unix.SOL_SOCKET {
			valid = false
			continue
		}
		switch header.Type {
		case unix.SCM_CREDENTIALS:
			credentials++
			if len(data) != unix.SizeofUcred {
				valid = false
				continue
			}
			message := unix.SocketControlMessage{Header: header, Data: data}
			credential, err := unix.ParseUnixCredentials(&message)
			if err != nil || credential.Pid <= 0 {
				valid = false
				continue
			}
			sender = Sender{PID: credential.Pid, UID: credential.Uid, GID: credential.Gid}
		case unix.SCM_PIDFD, unix.SCM_RIGHTS:
			// Account for every complete installed descriptor even on rejection/truncation.
			for i := 0; i+4 <= len(data); i += 4 {
				number := int(int32(binary.NativeEndian.Uint32(data[i : i+4])))
				if number >= 0 {
					descriptors[number] = true
				}
			}
			if header.Type == unix.SCM_RIGHTS {
				valid = false
				continue
			}
			pidfds++
			if len(data) != 4 {
				valid = false
				continue
			}
			pidfd = int(int32(binary.NativeEndian.Uint32(data)))
			if pidfd < 0 {
				valid = false
			}
		default:
			valid = false
		}
	}
	if credentials != 1 || pidfds != 1 || pidfd < 0 {
		valid = false
	}
	if valid {
		flags, err := unix.FcntlInt(uintptr(pidfd), unix.F_GETFD, 0)
		if err != nil || flags&unix.FD_CLOEXEC == 0 {
			valid = false
		}
	}
	for fd := range descriptors {
		if !valid || fd != pidfd {
			unix.Close(fd)
		}
	}
	if !valid {
		return Sender{}, -1, false
	}
	return sender, pidfd, true
}
