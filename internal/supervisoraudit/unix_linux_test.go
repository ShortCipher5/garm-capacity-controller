//go:build linux && cgo

package supervisoraudit

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func socketPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, e := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if e != nil {
		t.Fatal(e)
	}
	connections := make([]*net.UnixConn, 0, 2)
	for _, fd := range fds {
		f := os.NewFile(uintptr(fd), "audit-test-socket")
		c, e := net.FileConn(f)
		f.Close()
		if e != nil {
			t.Fatal(e)
		}
		connections = append(connections, c.(*net.UnixConn))
	}
	t.Cleanup(func() {
		for _, c := range connections {
			c.Close()
		}
	})
	return connections[0], connections[1]
}
func requestBody(j *Journal, n int) []byte {
	return []byte(fmt.Sprintf(`{"protocol":"%s","command":"record_claim","history_id":"%s","scope_digest":"%s","attempt_digest":"%s"}`, AuditProtocol, j.HistoryID(), claim(n), claim(n+1000)))
}
func packet(body []byte) []byte {
	out := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(out, uint32(len(body)))
	copy(out[4:], body)
	return out
}
func recorder(t *testing.T, j *Journal) *UnixRecorder {
	t.Helper()
	r, e := NewUnixRecorder(j, uint32(os.Geteuid()))
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func roundTrip(t *testing.T, r *UnixRecorder, body []byte) ([]byte, error) {
	t.Helper()
	server, client := socketPair(t)
	done := make(chan error, 1)
	go func() { done <- r.RecordConnection(context.Background(), server) }()
	if _, e := client.Write(packet(body)); e != nil {
		t.Fatal(e)
	}
	client.CloseWrite()
	response, _ := io.ReadAll(client)
	return response, <-done
}
func response(t *testing.T, raw []byte) auditResponse {
	t.Helper()
	if len(raw) < 4 || int(binary.BigEndian.Uint32(raw[:4])) != len(raw)-4 {
		t.Fatalf("invalid response frame (%d bytes)", len(raw))
	}
	var a auditResponse
	if e := json.Unmarshal(raw[4:], &a); e != nil {
		t.Fatal(e)
	}
	if a.ActivationAuthorized || a.ExecutionAuthority != "none" || a.Boundary != Boundary || a.Outcome != "recorded_history" || a.Protocol != AuditProtocol {
		t.Fatal("response incorrectly grants authority", a)
	}
	var fields map[string]json.RawMessage
	json.Unmarshal(raw[4:], &fields)
	if len(fields) != 10 || string(fields["activation_authorized"]) != "false" {
		t.Fatal("missing explicit permanent no-action boundary")
	}
	return a
}
func TestUnixAuditRecordReplayAndConflict(t *testing.T) {
	j, _ := setup(t)
	r := recorder(t, j)
	first, e := roundTrip(t, r, requestBody(j, 1))
	if e != nil {
		t.Fatal(e)
	}
	a := response(t, first)
	if a.Replay || a.Sequence != 1 || a.History != j.HistoryID() {
		t.Fatal(a)
	}
	raw, e := roundTrip(t, r, requestBody(j, 1))
	if e != nil {
		t.Fatal(e)
	}
	b := response(t, raw)
	if !b.Replay || b.Sequence != a.Sequence {
		t.Fatal(b)
	}
	changed := strings.Replace(string(requestBody(j, 1)), claim(1001), claim(9999), 1)
	raw, e = roundTrip(t, r, []byte(changed))
	if !errors.Is(e, ErrConflict) || len(raw) != 0 {
		t.Fatal("conflict escaped", e, string(raw))
	}
	s, e := j.Snapshot()
	if e != nil || s.Total != 1 {
		t.Fatal(s, e)
	}
}
func TestUnixAuditStrictRequestRefusal(t *testing.T) {
	j, _ := setup(t)
	r := recorder(t, j)
	base := string(requestBody(j, 1))
	cases := map[string]string{
		"unknown":          strings.TrimSuffix(base, "}") + `,"uid":0}`,
		"casefold":         strings.Replace(base, `"protocol"`, `"Protocol"`, 1),
		"duplicate":        strings.TrimSuffix(base, "}") + `,"protocol":"` + AuditProtocol + `"}`,
		"escapedduplicate": strings.TrimSuffix(base, "}") + `,"\u0070rotocol":"` + AuditProtocol + `"}`,
		"null":             strings.Replace(base, `"record_claim"`, `null`, 1),
		"bool":             strings.Replace(base, `"record_claim"`, `true`, 1),
		"number":           strings.Replace(base, `"record_claim"`, `42`, 1),
		"command":          strings.Replace(base, `"record_claim"`, `"launch"`, 1),
		"protocol":         strings.Replace(base, AuditProtocol, "supervisor-audit/v2", 1),
		"history":          strings.Replace(base, j.HistoryID(), claim(999), 1),
		"uppercase":        strings.Replace(base, claim(1), strings.Repeat("A", 64), 1),
		"missing":          strings.Replace(base, `"scope_digest":"`+claim(1)+`",`, "", 1),
		"tail":             base + ` {}`,
		"array":            "[]",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			raw, e := roundTrip(t, r, []byte(body))
			if !errors.Is(e, ErrProtocol) || len(raw) != 0 {
				t.Fatal(e, string(raw))
			}
		})
	}
	s, e := j.Snapshot()
	if e != nil || s.Total != 0 {
		t.Fatal(s, e)
	}
}
func TestUnixAuditFrameRefusal(t *testing.T) {
	j, _ := setup(t)
	r := recorder(t, j)
	cases := map[string][]byte{"zero": {0, 0, 0, 0}, "oversize": {0, 0, 16, 1}, "shortheader": {0, 0}, "shortbody": {0, 0, 0, 9, '{'}, "pipeline": append(packet(requestBody(j, 1)), packet(requestBody(j, 2))...)}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			server, client := socketPair(t)
			done := make(chan error, 1)
			go func() { done <- r.RecordConnection(context.Background(), server) }()
			client.Write(p)
			client.CloseWrite()
			b, _ := io.ReadAll(client)
			if e := <-done; e != ErrProtocol || len(b) != 0 {
				t.Fatal(e, string(b))
			}
		})
	}
	s, e := j.Snapshot()
	if e != nil || s.Total != 0 {
		t.Fatal(s, e)
	}
}
func TestUnixAuditPeerDeniedBeforeReading(t *testing.T) {
	j, _ := setup(t)
	r, e := NewUnixRecorder(j, uint32(os.Geteuid())+1)
	if e != nil {
		t.Fatal(e)
	}
	server, client := socketPair(t)
	defer client.Close()
	done := make(chan error, 1)
	go func() { done <- r.RecordConnection(context.Background(), server) }()
	select {
	case e := <-done:
		if e != ErrPeer {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("peer refusal waited for a request")
	}
	s, e := j.Snapshot()
	if e != nil || s.Total != 0 {
		t.Fatal(s, e)
	}
}
func TestUnixAuditCancellationAndMissingHalfClose(t *testing.T) {
	for _, send := range []bool{false, true} {
		t.Run(fmt.Sprint(send), func(t *testing.T) {
			j, _ := setup(t)
			r := recorder(t, j)
			server, client := socketPair(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- r.RecordConnection(ctx, server) }()
			if send {
				client.Write(packet(requestBody(j, 1)))
			} // Deliberately do not half-close.
			select {
			case e := <-done:
				if e == nil {
					t.Fatal("unterminated frame accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("cancel did not close owned socket")
			}
			s, e := j.Snapshot()
			if e != nil || s.Total != 0 {
				t.Fatal(s, e)
			}
		})
	}
}
func TestUnixAuditLostReplyAndUnknownCommit(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprint(uncertain), func(t *testing.T) {
			j, path := setup(t)
			history := j.HistoryID()
			r := recorder(t, j)
			server, client := socketPair(t)
			j.commit = func(c *sql.Conn) error {
				_, e := c.ExecContext(context.Background(), "COMMIT")
				if e != nil {
					return e
				}
				client.Close()
				if uncertain {
					return errors.New("simulated lost commit response")
				}
				return nil
			}
			done := make(chan error, 1)
			go func() { done <- r.RecordConnection(context.Background(), server) }()
			if _, e := client.Write(packet(requestBody(j, 1))); e != nil {
				t.Fatal(e)
			}
			client.CloseWrite()
			e := <-done
			if uncertain && e != ErrUncertain {
				t.Fatal(e)
			}
			if !uncertain && e != ErrTransport {
				t.Fatal(e)
			}
			if uncertain {
				if _, e = j.Snapshot(); e == nil {
					t.Fatal("poisoned journal accepted read")
				}
			}
			j.Close()
			restored, e := OpenExisting(path, history)
			if e != nil {
				t.Fatal(e)
			}
			defer restored.Close()
			raw, e := roundTrip(t, recorder(t, restored), requestBody(restored, 1))
			if e != nil {
				t.Fatal(e)
			}
			if a := response(t, raw); !a.Replay || a.Sequence != 1 {
				t.Fatal(a)
			}
		})
	}
}
func TestUnixAuditRejectsAndClosesAncillaryRights(t *testing.T) {
	for _, count := range []int{1, 100} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			j, _ := setup(t)
			r := recorder(t, j)
			server, client := socketPair(t)
			f, e := os.CreateTemp(t.TempDir(), "sentinel")
			if e != nil {
				t.Fatal(e)
			}
			defer f.Close()
			fds := make([]int, count)
			for i := range fds {
				fds[i] = int(f.Fd())
			}
			done := make(chan error, 1)
			go func() { done <- r.RecordConnection(context.Background(), server) }()
			if _, _, e = client.WriteMsgUnix(packet(requestBody(j, 1)), syscall.UnixRights(fds...), nil); e != nil {
				t.Fatal(e)
			}
			client.CloseWrite()
			io.ReadAll(client)
			if e = <-done; e != ErrProtocol {
				t.Fatal(e)
			}
			links, e := os.ReadDir("/proc/self/fd")
			if e != nil {
				t.Fatal(e)
			}
			matches := 0
			for _, link := range links {
				target, _ := os.Readlink("/proc/self/fd/" + link.Name())
				if target == f.Name() {
					matches++
				}
			}
			if matches != 1 {
				t.Fatalf("delivered rights leaked or original descriptor closed: %d", matches)
			}
			s, e := j.Snapshot()
			if e != nil || s.Total != 0 {
				t.Fatal(s, e)
			}
		})
	}
}

// Children are only this package's own Go-test binary acting as audit clients.
// No service, shell, namespace/container/VM or privileged action is started.
func TestUnixAuditChild(t *testing.T) {
	mode := os.Getenv("SUPERVISOR_AUDIT_TEST_CHILD")
	if mode == "" {
		return
	}
	var c *net.UnixConn
	if mode == "dial" {
		var e error
		c, e = net.DialUnix("unix", nil, &net.UnixAddr{Name: os.Getenv("SUPERVISOR_AUDIT_TEST_PATH"), Net: "unix"})
		if e != nil {
			t.Fatal(e)
		}
	} else if mode == "fd" {
		f := os.NewFile(3, "own-test-socket")
		conn, e := net.FileConn(f)
		f.Close()
		if e != nil {
			t.Fatal(e)
		}
		c = conn.(*net.UnixConn)
	} else {
		t.Fatal("invalid own-test mode")
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, e := c.Write(packet([]byte(os.Getenv("SUPERVISOR_AUDIT_TEST_BODY")))); e != nil {
		t.Fatal(e)
	}
	c.CloseWrite()
	raw, e := io.ReadAll(c)
	if e != nil {
		t.Fatal(e)
	}
	response(t, raw)
}
func childCommand(t *testing.T, ctx context.Context, mode string, body []byte) *exec.Cmd {
	t.Helper()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	c := exec.CommandContext(ctx, exe, "-test.run=^TestUnixAuditChild$")
	c.Env = []string{"SUPERVISOR_AUDIT_TEST_CHILD=" + mode, "SUPERVISOR_AUDIT_TEST_BODY=" + string(body)}
	return c
}
func TestUnixAuditActualChildPeerAndFDDelegationLimit(t *testing.T) {
	t.Run("actual_connecting_child", func(t *testing.T) {
		requireNamedUnixEndpoint(t)
		j, _ := setup(t)
		r := recorder(t, j)
		dir := t.TempDir()
		if e := os.Chmod(dir, 0700); e != nil {
			t.Fatal(e)
		}
		path := filepath.Join(dir, "audit.sock")
		listener, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if e != nil {
			t.Fatal(e)
		}
		defer listener.Close()
		if e := os.Chmod(path, 0600); e != nil {
			t.Fatal(e)
		}
		listener.SetDeadline(time.Now().Add(5 * time.Second))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c := childCommand(t, ctx, "dial", requestBody(j, 1))
		c.Env = append(c.Env, "SUPERVISOR_AUDIT_TEST_PATH="+path)
		if e = c.Start(); e != nil {
			t.Fatal(e)
		}
		conn, e := listener.AcceptUnix()
		if e != nil {
			t.Fatal(e)
		}
		cred, e := unixCredentials(conn)
		if e != nil || int(cred.Pid) != c.Process.Pid || cred.Uid != uint32(os.Geteuid()) {
			t.Fatal(cred, e)
		}
		if e = r.RecordConnection(ctx, conn); e != nil {
			t.Fatal(e)
		}
		if e = c.Wait(); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("delegated_FD_still_reports_original_parent", func(t *testing.T) {
		j, _ := setup(t)
		r := recorder(t, j)
		server, client := socketPair(t)
		file, e := client.File()
		if e != nil {
			t.Fatal(e)
		}
		defer file.Close()
		client.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c := childCommand(t, ctx, "fd", requestBody(j, 1))
		c.ExtraFiles = []*os.File{file}
		if e = c.Start(); e != nil {
			t.Fatal(e)
		}
		cred, e := unixCredentials(server)
		if e != nil || int(cred.Pid) != os.Getpid() || int(cred.Pid) == c.Process.Pid {
			t.Fatal(cred, e)
		}
		// Real negative counterexample: a matching SO_PEERCRED is NOT proof of the
		// current descriptor holder or a protected control/job isolation boundary.
		if e = r.RecordConnection(ctx, server); e != nil {
			t.Fatal(e)
		}
		if e = c.Wait(); e != nil {
			t.Fatal(e)
		}
	})
}
func TestUnixAuditPathIsNotAnIsolationAttestation(t *testing.T) {
	requireNamedUnixEndpoint(t)
	j, _ := setup(t)
	r := recorder(t, j)
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir, "audit.sock")
	listener, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	// Only this test's private parent is accessible. Deliberately loose socket
	// mode and a symlink demonstrate what a connection-only adapter cannot attest.
	if e := os.Chmod(path, 0777); e != nil {
		t.Fatal(e)
	}
	alias := filepath.Join(dir, "alias.sock")
	if e = os.Symlink(path, alias); e != nil {
		t.Fatal(e)
	}
	client, e := net.DialUnix("unix", nil, &net.UnixAddr{Name: alias, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	conn, e := listener.AcceptUnix()
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- r.RecordConnection(context.Background(), conn) }()
	client.Write(packet(requestBody(j, 1)))
	client.CloseWrite()
	raw, e := io.ReadAll(client)
	if e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	response(t, raw)
	// It records history, not a protected filesystem endpoint or action permit.
}

func requireNamedUnixEndpoint(t *testing.T) {
	t.Helper()
	if os.Getenv("GCC_AUDIT_UNIX_BIND_TESTS") != "1" {
		t.Skip("named Unix endpoint tests require explicit disposable environment; socketpair results are NOT pathname/bind qualification")
	}
}

func TestUnixAuditConcurrentConnectionsAndHandles(t *testing.T) {
	j, path := setup(t)
	other, e := OpenExisting(path, j.HistoryID())
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	recorders := []*UnixRecorder{recorder(t, j), recorder(t, other)}
	results := make(chan auditResponse, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raw, e := roundTrip(t, recorders[i%2], requestBody(j, 1))
			if e != nil {
				t.Error(e)
				return
			}
			results <- response(t, raw)
		}(i)
	}
	wg.Wait()
	close(results)
	fresh, total := 0, 0
	for a := range results {
		total++
		if !a.Replay {
			fresh++
		}
		if a.Sequence != 1 {
			t.Fatal(a)
		}
	}
	if total != 12 || fresh != 1 {
		t.Fatal("unexpected history acknowledgments", total, fresh)
	}
	s, e := j.Snapshot()
	if e != nil || s.Total != 1 {
		t.Fatal(s, e)
	}
}
func TestUnixAuditClosedOrCancelledNeverAcknowledges(t *testing.T) {
	j, _ := setup(t)
	r := recorder(t, j)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, c := socketPair(t)
	if e := r.RecordConnection(ctx, s); e != ErrTransport {
		t.Fatal(e)
	}
	raw, _ := io.ReadAll(c)
	if len(raw) != 0 {
		t.Fatal("cancelled receiver acknowledged")
	}
	snapshot, e := j.Snapshot()
	if e != nil || snapshot.Total != 0 {
		t.Fatal(snapshot, e)
	}
	j.Close()
	raw, e = roundTrip(t, r, requestBody(j, 1))
	if e == nil || len(raw) != 0 {
		t.Fatal("closed history acknowledged", e)
	}
	if _, e = NewUnixRecorder(j, uint32(os.Geteuid())); e == nil {
		t.Fatal("closed history reopened by recorder")
	}
	if _, e = NewUnixRecorder(nil, uint32(os.Geteuid())); e != ErrHistory {
		t.Fatal(e)
	}
}

func TestUnixAuditCancellationAfterCommitRetainsHistory(t *testing.T) {
	j, _ := setup(t)
	r := recorder(t, j)
	server, client := socketPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	j.commit = func(c *sql.Conn) error { _, e := c.ExecContext(context.Background(), "COMMIT"); cancel(); return e }
	done := make(chan error, 1)
	go func() { done <- r.RecordConnection(ctx, server) }()
	client.Write(packet(requestBody(j, 1)))
	client.CloseWrite()
	raw, _ := io.ReadAll(client)
	if e := <-done; e != ErrTransport || len(raw) != 0 {
		t.Fatal("cancelled commit acknowledged", e)
	}
	j.commit = nil
	s, e := j.Snapshot()
	if e != nil || s.Total != 1 {
		t.Fatal("cancel discarded committed fact", s, e)
	}
	raw, e = roundTrip(t, r, requestBody(j, 1))
	if e != nil {
		t.Fatal(e)
	}
	if a := response(t, raw); !a.Replay {
		t.Fatal(a)
	}
}
