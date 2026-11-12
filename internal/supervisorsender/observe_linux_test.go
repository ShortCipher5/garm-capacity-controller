//go:build linux

package supervisorsender

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func pair(t *testing.T, kind int) (*os.File, *os.File) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, kind|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		if os.Getenv("GCC_SENDER_PIDFD_TESTS") == "1" {
			t.Fatalf("required owned Unix socketpair unavailable: %v", err)
		}
		t.Skipf("owned Unix socketpair unavailable: %v", err)
	}
	a := os.NewFile(uintptr(fds[0]), "receiver")
	b := os.NewFile(uintptr(fds[1]), "sender")
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

func packetPair(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	a, b := pair(t, unix.SOCK_SEQPACKET)
	if err := PrepareEndpoint(a); err != nil {
		if os.Getenv("GCC_SENDER_PIDFD_TESTS") == "1" {
			t.Fatal("required packet credentials/pidfd unavailable")
		}
		t.Skip("packet credentials/pidfd unavailable; not qualified")
	}
	return a, b
}

func send(t *testing.T, file *os.File, data, oob []byte) {
	t.Helper()
	n, err := unix.SendmsgN(int(file.Fd()), data, oob, nil, 0)
	if err != nil || n != len(data) {
		t.Fatalf("owned packet send: n=%d err=%v", n, err)
	}
}

func TestPacketSenderActualCredentials(t *testing.T) {
	receiver, sender := packetPair(t)
	send(t, sender, []byte("untrusted-data"), nil)
	observation, err := Observe(context.Background(), receiver, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer observation.Close()
	credential, data, ok := observation.Snapshot()
	if !ok || credential.PID != int32(os.Getpid()) || credential.UID != uint32(os.Getuid()) || credential.GID != uint32(os.Getgid()) || string(data) != "untrusted-data" {
		t.Fatalf("%+v %q %v", credential, data, ok)
	}
	flags, err := unix.FcntlInt(uintptr(observation.state.fd), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("missing pidfd close-on-exec")
	}
	data[0] = 'x'
	credential.PID = -1
	copied := observation
	credential, data, ok = copied.Snapshot()
	if !ok || credential.PID != int32(os.Getpid()) || string(data) != "untrusted-data" {
		t.Fatal("mutable snapshot")
	}
	if err := copied.Close(); err != nil {
		t.Fatal(err)
	}
	if _, data, ok := observation.Snapshot(); ok || data != nil {
		t.Fatal("copy did not share close")
	}
	if err := observation.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPacketSenderDelegatedFDShowsChildNotConnectionPeer(t *testing.T) {
	receiver, sender := packetPair(t)
	connectionPeer, err := unix.GetsockoptUcred(int(receiver.Fd()), unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		t.Fatal(err)
	}
	if connectionPeer.Pid != int32(os.Getpid()) {
		t.Fatal("expected parent connection-time credential")
	}
	ackReader, ackWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer ackReader.Close()
	executable, err := os.Executable()
	if err != nil {
		ackWriter.Close()
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^TestPacketSenderChild$", "-test.count=1")
	child.Env = []string{"GCC_PACKET_SENDER_HELPER=1"}
	child.ExtraFiles = []*os.File{sender, ackReader}
	var output, diagnostics bytes.Buffer
	child.Stdin = bytes.NewReader(nil)
	child.Stdout = &output
	child.Stderr = &diagnostics
	if err := child.Start(); err != nil {
		ackWriter.Close()
		t.Fatal(err)
	}
	defer func() {
		ackWriter.Close()
		if err := child.Wait(); err != nil {
			t.Errorf("owned child failed: %v %s", err, diagnostics.String())
		}
	}()
	observation, err := Observe(context.Background(), receiver, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer observation.Close()
	credential, payload, ok := observation.Snapshot()
	if !ok || credential.PID != int32(child.Process.Pid) || credential.PID == connectionPeer.Pid || credential.UID != uint32(os.Getuid()) || string(payload) != "claimed-parent-role" {
		t.Fatalf("sender %+v payload %q valid %v", credential, payload, ok)
	}
	// The inherited connection still has the parent's SO_PEERCRED, but actual
	// per-packet identity is the child. Payload role text cannot authenticate code.
	if _, err := ackWriter.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
}

func TestPacketSenderChild(t *testing.T) {
	if os.Getenv("GCC_PACKET_SENDER_HELPER") != "1" {
		return
	}
	sender := os.NewFile(3, "inherited-packet-sender")
	defer sender.Close()
	ack := os.NewFile(4, "finish-ack")
	defer ack.Close()
	send(t, sender, []byte("claimed-parent-role"), nil)
	var b [1]byte
	if _, err := ack.Read(b[:]); err != nil {
		t.Fatal(err)
	}
}

func descriptorCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestPacketSenderRightsAndTruncationCloseDescriptors(t *testing.T) {
	receiver, sender := packetPair(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	for _, test := range []struct {
		name               string
		payload            []byte
		oob                []byte
		maximum, ancillary int
	}{
		{"rights", []byte("data"), unix.UnixRights(int(reader.Fd())), 64, unix.CmsgSpace(unix.SizeofUcred) + unix.CmsgSpace(4) + unix.CmsgSpace(253*4)},
		{"truncated-rights", []byte("data"), unix.UnixRights(int(reader.Fd()), int(writer.Fd()), int(reader.Fd()), int(writer.Fd())), 64, unix.CmsgSpace(unix.SizeofUcred) + unix.CmsgSpace(4)},
		{"truncated-payload", bytes.Repeat([]byte("a"), 100), nil, 4, 128},
		{"zero-payload", nil, nil, 64, 128},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := descriptorCount(t)
			send(t, sender, test.payload, test.oob)
			observation, err := observe(context.Background(), receiver, test.maximum, test.ancillary)
			if !errors.Is(err, ErrObservation) {
				observation.Close()
				t.Fatalf("unexpected %v", err)
			}
			if _, data, ok := observation.Snapshot(); ok || data != nil {
				t.Fatal("nonzero error observation")
			}
			after := descriptorCount(t)
			if before != after {
				t.Fatalf("descriptor leak before=%d after=%d", before, after)
			}
		})
	}
}

func TestPacketSenderBoundsCancellationAndWrongEndpoint(t *testing.T) {
	receiver, _ := packetPair(t)
	for _, maximum := range []int{-1, 0, MaxPayload + 1} {
		observation, err := Observe(context.Background(), receiver, maximum)
		if !errors.Is(err, ErrObservation) || observation.state != nil {
			t.Fatal("invalid maximum accepted")
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	observation, err := Observe(cancelled, receiver, 64)
	if !errors.Is(err, ErrObservation) || observation.state != nil {
		t.Fatal("cancelled accepted")
	}
	timeout, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	observation, err = Observe(timeout, receiver, 64)
	if !errors.Is(err, ErrObservation) || observation.state != nil || time.Since(start) > time.Second {
		t.Fatal("bounded empty receive failed")
	}
	stream, _ := pair(t, unix.SOCK_STREAM)
	if err := PrepareEndpoint(stream); !errors.Is(err, ErrEndpoint) {
		t.Fatal("stream accepted")
	}
	if err := PrepareEndpoint(nil); !errors.Is(err, ErrEndpoint) {
		t.Fatal("nil accepted")
	}
	observation, err = Observe(context.Background(), nil, 64)
	if !errors.Is(err, ErrEndpoint) || observation.state != nil {
		t.Fatal("nil observation")
	}
	var zero Observation
	if _, data, ok := zero.Snapshot(); ok || data != nil {
		t.Fatal("zero observed")
	}
	if err := zero.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPacketSenderSnapshotConcurrentClose(t *testing.T) {
	receiver, sender := packetPair(t)
	send(t, sender, []byte("data"), nil)
	observation, err := Observe(context.Background(), receiver, 64)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 32; j++ {
				_, data, ok := observation.Snapshot()
				if ok && string(data) != "data" {
					t.Errorf("bad snapshot %q", data)
				}
			}
		}()
	}
	if err := observation.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}

// Only owned duplicate descriptors are encoded here. Never fuzz arbitrary fd
// integers into a cleanup parser: they could name unrelated test-process fds.
func TestPacketSenderMalformedAncillaryClosesOwnedDescriptors(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	for _, name := range []string{"missing-credentials", "duplicate-pidfd", "duplicate-credentials", "short-credentials", "trailing-malformed"} {
		t.Run(name, func(t *testing.T) {
			first, err := unix.Dup(int(reader.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			second, err := unix.Dup(int(reader.Fd()))
			if err != nil {
				unix.Close(first)
				t.Fatal(err)
			}
			// SCM_PIDFD is receive-only kernel metadata in production. These synthetic
			// controls exercise refusal/cleanup only; no fake pidfd is ever accepted.
			pidControl := func(fd int) []byte {
				b := unix.UnixRights(fd)
				(*unix.Cmsghdr)(unsafe.Pointer(&b[0])).Type = unix.SCM_PIDFD
				return b
			}
			credential := unix.UnixCredentials(&unix.Ucred{Pid: int32(os.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())})
			controls := append([]byte(nil), pidControl(first)...)
			switch name {
			case "missing-credentials":
			case "duplicate-pidfd":
				controls = append(controls, pidControl(second)...)
				controls = append(controls, credential...)
			case "duplicate-credentials":
				controls = append(controls, credential...)
				controls = append(controls, credential...)
			case "short-credentials":
				short := unix.UnixRights()
				(*unix.Cmsghdr)(unsafe.Pointer(&short[0])).Type = unix.SCM_CREDENTIALS
				controls = append(controls, short...)
			case "trailing-malformed":
				controls = append(controls, credential...)
				controls = append(controls, 0)
			}
			if name != "duplicate-pidfd" {
				unix.Close(second)
			}
			credentialValue, fd, ok := parseAncillary(controls)
			if ok || fd != -1 || credentialValue != (Sender{}) {
				if fd >= 0 {
					unix.Close(fd)
				}
				t.Fatal("malformed ancillary accepted")
			}
			if _, err := unix.FcntlInt(uintptr(first), unix.F_GETFD, 0); err != unix.EBADF {
				t.Fatal("first installed descriptor not closed")
			}
			if name == "duplicate-pidfd" {
				if _, err := unix.FcntlInt(uintptr(second), unix.F_GETFD, 0); err != unix.EBADF {
					t.Fatal("second installed descriptor not closed")
				}
			}
		})
	}
}
