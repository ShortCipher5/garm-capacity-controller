//go:build linux && cgo

package supervisoraudit

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func observePair(t *testing.T, j *Journal) (UnixHistoryReceipt, error) {
	t.Helper()
	s, c := socketPair(t)
	r := recorder(t, j)
	done := make(chan error, 1)
	go func() { done <- r.RecordConnection(context.Background(), s) }()
	a, e := ObserveUnixClaim(context.Background(), c, uint32(os.Geteuid()), j.HistoryID(), claim(1), claim(1001))
	serverErr := <-done
	if e == nil && serverErr != nil {
		t.Fatal(serverErr)
	}
	return a, e
}
func assertNoReceipt(t *testing.T, a UnixHistoryReceipt, e error) {
	t.Helper()
	if e == nil || a != (UnixHistoryReceipt{}) || a.ActivationAuthorized() || a.ExecutionAuthority() != "none" || a.Boundary() != Boundary {
		t.Fatal("failed exchange exposed receipt or action authority", a, e)
	}
}
func validReply(req auditRequest) []byte {
	b, _ := json.Marshal(auditResponse{AuditProtocol, "recorded_history", req.history, 1, req.scope, req.attempt, false, Boundary, false, "none"})
	return b
}
func fakeReply(t *testing.T, j *Journal, raw []byte) (UnixHistoryReceipt, error) {
	t.Helper()
	s, c := socketPair(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer s.Close()
		if _, e := readAuditFrame(s); e == nil {
			s.Write(raw)
		}
	}()
	a, e := ObserveUnixClaim(context.Background(), c, uint32(os.Geteuid()), j.HistoryID(), claim(1), claim(1001))
	<-done
	return a, e
}
func TestUnixAuditClientActualJournalAndImmutableReceipt(t *testing.T) {
	j, _ := setup(t)
	a, e := observePair(t, j)
	if e != nil {
		t.Fatal(e)
	}
	if !a.ObservedHistory() || a.HistoryID() != j.HistoryID() || a.Replay() || a.ServerConnectionUID() != uint32(os.Geteuid()) || a.ServerConnectionPID() != int32(os.Getpid()) || a.ActivationAuthorized() || a.ExecutionAuthority() != "none" {
		t.Fatal(a)
	}
	fact := a.Fact()
	fact.Sequence = 999
	fact.ScopeDigest = claim(999)
	if a.Fact().Sequence != 1 || a.Fact().ScopeDigest != claim(1) {
		t.Fatal("receipt changed through accessor copy")
	}
	b, e := observePair(t, j)
	if e != nil || !b.Replay() || b.Fact() != a.Fact() {
		t.Fatal(b, e)
	}
	// JSON transcripts cannot construct the private observed state.
	raw, _ := json.Marshal(a)
	var copied UnixHistoryReceipt
	if string(raw) != "{}" || json.Unmarshal(validReply(auditRequest{history: j.HistoryID(), scope: claim(1), attempt: claim(1001)}), &copied) != nil || copied.ObservedHistory() {
		t.Fatal("raw JSON manufactured opaque observation")
	}
	if copied.ActivationAuthorized() || copied.ExecutionAuthority() != "none" {
		t.Fatal(copied)
	}
}
func TestUnixAuditClientServerUIDCheckedBeforeSending(t *testing.T) {
	j, _ := setup(t)
	s, c := socketPair(t)
	done := make(chan []byte, 1)
	go func() { b, _ := io.ReadAll(s); done <- b }()
	a, e := ObserveUnixClaim(context.Background(), c, uint32(os.Geteuid())+1, j.HistoryID(), claim(1), claim(1001))
	assertNoReceipt(t, a, e)
	if e != ErrPeer {
		t.Fatal(e)
	}
	if sent := <-done; len(sent) != 0 {
		t.Fatal("claim transmitted before server UID check")
	}
	snapshot, e := j.Snapshot()
	if e != nil || snapshot.Total != 0 {
		t.Fatal(snapshot, e)
	}
}
func TestUnixAuditClientStrictAcknowledgments(t *testing.T) {
	j, _ := setup(t)
	req := auditRequest{history: j.HistoryID(), scope: claim(1), attempt: claim(1001)}
	base := validReply(req)
	var original map[string]json.RawMessage
	if json.Unmarshal(base, &original) != nil {
		t.Fatal("fixture")
	}
	cases := map[string][]byte{}
	for key := range original {
		for _, kind := range []string{"missing", "null", "wrong_type"} {
			fields := map[string]json.RawMessage{}
			for k, v := range original {
				fields[k] = v
			}
			switch kind {
			case "missing":
				delete(fields, key)
			case "null":
				fields[key] = json.RawMessage("null")
			case "wrong_type":
				fields[key] = json.RawMessage(`[]`)
			}
			body, _ := json.Marshal(fields)
			cases[kind+"_"+key] = packet(body)
		}
	}
	for key, value := range map[string]string{"protocol": `"supervisor-audit/v2"`, "outcome": `"activated"`, "history_id": `"` + claim(999) + `"`, "scope_digest": `"` + claim(2) + `"`, "attempt_digest": `"` + claim(1002) + `"`, "boundary": `"trusted"`, "activation_authorized": `true`, "execution_authority": `"launch"`, "sequence": `0`} {
		fields := map[string]json.RawMessage{}
		for k, v := range original {
			fields[k] = v
		}
		fields[key] = json.RawMessage(value)
		body, _ := json.Marshal(fields)
		cases["mismatch_"+key] = packet(body)
	}
	for name, value := range map[string]string{"negative": "-1", "fraction": "1.0", "exponent": "1e0", "quoted": `"1"`, "overflow": "9223372036854775808", "beyond_history_cap": fmt.Sprint(MaxFacts + 1)} {
		fields := map[string]json.RawMessage{}
		for k, v := range original {
			fields[k] = v
		}
		fields["sequence"] = json.RawMessage(value)
		body, _ := json.Marshal(fields)
		cases[name] = packet(body)
	}
	cases["unknown"] = packet([]byte(strings.TrimSuffix(string(base), "}") + `,"server_uid":0}`))
	cases["casefold"] = packet([]byte(strings.Replace(string(base), `"protocol"`, `"Protocol"`, 1)))
	cases["duplicate"] = packet([]byte(strings.TrimSuffix(string(base), "}") + `,"protocol":"` + AuditProtocol + `"}`))
	cases["escaped_duplicate"] = packet([]byte(strings.TrimSuffix(string(base), "}") + `,"\u0070rotocol":"` + AuditProtocol + `"}`))
	cases["trailing_json"] = packet(append(append([]byte{}, base...), []byte(" {}")...))
	cases["pipelined"] = append(packet(base), packet(base)...)
	cases["oversize"] = []byte{0, 0, 16, 1}
	cases["truncated"] = []byte{0, 0, 0, 9, '{'}
	cases["zero"] = []byte{0, 0, 0, 0}
	cases["empty"] = nil
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			a, e := fakeReply(t, j, raw)
			assertNoReceipt(t, a, e)
			if e != ErrAcknowledgment {
				t.Fatal(e)
			}
		})
	}
	snapshot, e := j.Snapshot()
	if e != nil || snapshot.Total != 0 {
		t.Fatal(snapshot, e)
	}
}
func TestUnixAuditClientSameUIDFabricationIsNotCommitProof(t *testing.T) {
	j, _ := setup(t)
	req := auditRequest{history: j.HistoryID(), scope: claim(1), attempt: claim(1001)}
	a, e := fakeReply(t, j, packet(validReply(req)))
	if e != nil || !a.ObservedHistory() || a.ActivationAuthorized() {
		t.Fatal(a, e)
	}
	snapshot, e := j.Snapshot()
	if e != nil || snapshot.Total != 0 {
		t.Fatal("counterfeit fixture unexpectedly journaled", snapshot, e)
	}
	// Real negative case: kernel UID and matching wire fields do NOT attest the
	// server's code, journal commit, enrollment or an external rollback anchor.
}
func TestUnixAuditClientCancelledAndUnterminatedReply(t *testing.T) {
	for _, before := range []bool{false, true} {
		t.Run(fmt.Sprint(before), func(t *testing.T) {
			j, _ := setup(t)
			s, c := socketPair(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if before {
				cancel()
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer s.Close()
				body, e := readAuditFrame(s)
				if e == nil {
					req, _ := parseAuditRequest(body)
					s.Write(packet(validReply(req)))
					<-ctx.Done()
				}
			}()
			// A complete JSON object is insufficient without framed response EOF.
			a, e := ObserveUnixClaim(ctx, c, uint32(os.Geteuid()), j.HistoryID(), claim(1), claim(1001))
			assertNoReceipt(t, a, e)
			<-done
		})
	}
}
func TestUnixAuditClientLostReplyAndUnknownCommitStayUnobserved(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprint(uncertain), func(t *testing.T) {
			j, path := setup(t)
			history := j.HistoryID()
			s, c := socketPair(t)
			r := recorder(t, j)
			j.commit = func(conn *sql.Conn) error {
				_, e := conn.ExecContext(context.Background(), "COMMIT")
				s.Close()
				if e != nil {
					return e
				}
				if uncertain {
					return errors.New("lost commit result")
				}
				return nil
			}
			done := make(chan error, 1)
			go func() { done <- r.RecordConnection(context.Background(), s) }()
			a, e := ObserveUnixClaim(context.Background(), c, uint32(os.Geteuid()), history, claim(1), claim(1001))
			assertNoReceipt(t, a, e)
			serverError := <-done
			if uncertain && serverError != ErrUncertain {
				t.Fatal(serverError)
			}
			if !uncertain && serverError != ErrTransport {
				t.Fatal(serverError)
			}
			j.Close()
			reopened, e := OpenExisting(path, history)
			if e != nil {
				t.Fatal(e)
			}
			defer reopened.Close()
			a, e = observePair(t, reopened)
			if e != nil || !a.Replay() || a.Fact().Sequence != 1 || a.ActivationAuthorized() {
				t.Fatal(a, e)
			}
		})
	}
}
func TestUnixAuditClientRejectsAncillaryRights(t *testing.T) {
	j, _ := setup(t)
	s, c := socketPair(t)
	file, e := os.CreateTemp(t.TempDir(), "client-received-right")
	if e != nil {
		t.Fatal(e)
	}
	defer file.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer s.Close()
		body, e := readAuditFrame(s)
		if e == nil {
			req, _ := parseAuditRequest(body)
			s.WriteMsgUnix(packet(validReply(req)), syscall.UnixRights(int(file.Fd())), nil)
		}
	}()
	a, e := ObserveUnixClaim(context.Background(), c, uint32(os.Geteuid()), j.HistoryID(), claim(1), claim(1001))
	assertNoReceipt(t, a, e)
	<-done
	entries, e := os.ReadDir("/proc/self/fd")
	if e != nil {
		t.Fatal(e)
	}
	count := 0
	for _, entry := range entries {
		target, _ := os.Readlink("/proc/self/fd/" + entry.Name())
		if target == file.Name() {
			count++
		}
	}
	if count != 1 {
		t.Fatal("client ancillary FD leak", count)
	}
}

func TestUnixAuditClientChild(t *testing.T) {
	mode := os.Getenv("SUPERVISOR_AUDIT_CLIENT_CHILD")
	if mode == "" {
		return
	}
	var c *net.UnixConn
	if mode == "listener" {
		path := os.Getenv("SUPERVISOR_AUDIT_CLIENT_SOCKET")
		listener, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if e != nil {
			t.Fatal(e)
		}
		defer listener.Close()
		if e = os.Chmod(path, 0600); e != nil {
			t.Fatal(e)
		}
		listener.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprintln(os.Stdout, "audit-listener-ready")
		c, e = listener.AcceptUnix()
		if e != nil {
			t.Fatal(e)
		}
		j, e := OpenExisting(os.Getenv("SUPERVISOR_AUDIT_CLIENT_DATABASE"), os.Getenv("SUPERVISOR_AUDIT_CLIENT_HISTORY"))
		if e != nil {
			t.Fatal(e)
		}
		defer j.Close()
		if e = recorder(t, j).RecordConnection(context.Background(), c); e != nil {
			t.Fatal(e)
		}
	} else if mode == "delegated_server" {
		f := os.NewFile(3, "own-test-server-socket")
		connection, e := net.FileConn(f)
		f.Close()
		if e != nil {
			t.Fatal(e)
		}
		c = connection.(*net.UnixConn)
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		body, e := readAuditFrame(c)
		if e != nil {
			t.Fatal(e)
		}
		request, e := parseAuditRequest(body)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = c.Write(packet(validReply(request))); e != nil {
			t.Fatal(e)
		}
	} else {
		t.Fatal("invalid own-test mode")
	}
}
func clientChildCommand(t *testing.T, ctx context.Context, mode string) *exec.Cmd {
	t.Helper()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	c := exec.CommandContext(ctx, exe, "-test.run=^TestUnixAuditClientChild$")
	c.Env = []string{"SUPERVISOR_AUDIT_CLIENT_CHILD=" + mode}
	return c
}
func TestUnixAuditClientActualChildServer(t *testing.T) {
	requireNamedUnixEndpoint(t)
	j, path := setup(t)
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	socket := filepath.Join(dir, "audit.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := clientChildCommand(t, ctx, "listener")
	cmd.Env = append(cmd.Env, "SUPERVISOR_AUDIT_CLIENT_SOCKET="+socket, "SUPERVISOR_AUDIT_CLIENT_DATABASE="+path, "SUPERVISOR_AUDIT_CLIENT_HISTORY="+j.HistoryID())
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	ready, e := bufio.NewReader(io.LimitReader(stdout, 1024)).ReadString('\n')
	if e != nil || ready != "audit-listener-ready\n" {
		t.Fatal("own child listener not ready", e)
	}
	conn, e := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	a, e := ObserveUnixClaim(ctx, conn, uint32(os.Geteuid()), j.HistoryID(), claim(1), claim(1001))
	if e != nil || !a.ObservedHistory() || a.ServerConnectionPID() != int32(cmd.Process.Pid) || a.ActivationAuthorized() {
		t.Fatal(a, e)
	}
	if e = cmd.Wait(); e != nil {
		t.Fatal(e)
	}
	snapshot, e := j.Snapshot()
	if e != nil || snapshot.Total != 1 {
		t.Fatal(snapshot, e)
	}
}
func TestUnixAuditClientDelegatedServerFDDoesNotAttestHolder(t *testing.T) {
	j, _ := setup(t)
	s, c := socketPair(t)
	file, e := s.File()
	if e != nil {
		t.Fatal(e)
	}
	defer file.Close()
	s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := clientChildCommand(t, ctx, "delegated_server")
	cmd.ExtraFiles = []*os.File{file}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	if e = file.Close(); e != nil {
		t.Fatal(e)
	}
	a, e := ObserveUnixClaim(ctx, c, uint32(os.Geteuid()), j.HistoryID(), claim(1), claim(1001))
	if e != nil || !a.ObservedHistory() || a.ServerConnectionPID() != int32(os.Getpid()) || a.ServerConnectionPID() == int32(cmd.Process.Pid) || a.ActivationAuthorized() {
		t.Fatal(a, e)
	}
	if e = cmd.Wait(); e != nil {
		t.Fatal(e)
	}
	snapshot, e := j.Snapshot()
	if e != nil || snapshot.Total != 0 {
		t.Fatal(snapshot, e)
	}
	// Same-UID child fabricates a response over an inherited parent connection;
	// peer credentials and opaque receipt still do NOT prove an actual commit.
}

func TestUnixAuditClientInvalidInputSendsNothing(t *testing.T) {
	for _, invalid := range []string{"", strings.Repeat("A", 64), claim(1) + "00"} {
		t.Run(fmt.Sprint(len(invalid)), func(t *testing.T) {
			j, _ := setup(t)
			s, c := socketPair(t)
			done := make(chan []byte, 1)
			go func() { b, _ := io.ReadAll(s); done <- b }()
			a, e := ObserveUnixClaim(context.Background(), c, uint32(os.Geteuid()), j.HistoryID(), invalid, claim(1001))
			assertNoReceipt(t, a, e)
			if sent := <-done; len(sent) != 0 {
				t.Fatal("invalid claim sent")
			}
		})
	}
}
