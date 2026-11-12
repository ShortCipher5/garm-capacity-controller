//go:build linux && cgo

package supervisoraudit

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"syscall"
	"time"
)

const AuditProtocol = "supervisor-audit/v1"
const MaxAuditFrame = 4096
const auditIOTimeout = 5 * time.Second

var (
	ErrPeer      = errors.New("supervisor audit: Unix peer refused")
	ErrProtocol  = errors.New("supervisor audit: invalid bounded audit request")
	ErrTransport = errors.New("supervisor audit: transport outcome unknown; history only")
)

// UnixRecorder is a non-executing adapter to local claim history. A matching UID
// authenticates only the connection's kernel-reported connect-time credentials,
// NOT enrollment, current FD holder, process birth, control/job isolation or a
// rollback anchor. Never connect this adapter to launch/activation/release.
//
// The caller owns listening/binding and must provide a protected Unix endpoint.
// This adapter accepts an already-connected descriptor; it neither creates nor
// qualifies filesystem/socket paths, modes, ownership or symlink resolution.
// A private pathname is not a hostile-same-UID/root boundary. M2 remains absent.
type UnixRecorder struct {
	journal *Journal
	uid     uint32
	slot    chan struct{}
}

func NewUnixRecorder(j *Journal, allowedUID uint32) (*UnixRecorder, error) {
	if j == nil {
		return nil, ErrHistory
	}
	if _, err := j.Snapshot(); err != nil {
		return nil, err
	}
	return &UnixRecorder{journal: j, uid: allowedUID, slot: make(chan struct{}, 1)}, nil
}

type auditRequest struct{ protocol, command, history, scope, attempt string }

type auditResponse struct {
	Protocol             string `json:"protocol"`
	Outcome              string `json:"outcome"`
	History              string `json:"history_id"`
	Sequence             int64  `json:"sequence"`
	Scope                string `json:"scope_digest"`
	Attempt              string `json:"attempt_digest"`
	Replay               bool   `json:"replay"`
	Boundary             string `json:"boundary"`
	ActivationAuthorized bool   `json:"activation_authorized"`
	ExecutionAuthority   string `json:"execution_authority"`
}

func parseAuditRequest(body []byte) (auditRequest, error) {
	var result auditRequest
	d := json.NewDecoder(bytes.NewReader(body))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return result, ErrProtocol
	}
	fields := map[string]*string{"protocol": &result.protocol, "command": &result.command, "history_id": &result.history, "scope_digest": &result.scope, "attempt_digest": &result.attempt}
	seen := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		target, known := fields[key]
		if err != nil || !ok || !known || seen[key] {
			return auditRequest{}, ErrProtocol
		}
		seen[key] = true
		var raw json.RawMessage
		if d.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, target) != nil {
			return auditRequest{}, ErrProtocol
		}
	}
	token, err = d.Token()
	var extra any
	if err != nil || token != json.Delim('}') || d.Decode(&extra) != io.EOF || len(seen) != len(fields) || result.protocol != AuditProtocol || result.command != "record_claim" || !digest(result.history) || !digest(result.scope) || !digest(result.attempt) {
		return auditRequest{}, ErrProtocol
	}
	return result, nil
}

func unixCredentials(c *net.UnixConn) (*syscall.Ucred, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return nil, ErrPeer
	}
	var cred *syscall.Ucred
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		kind, e := syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_TYPE)
		if e != nil || kind != syscall.SOCK_STREAM {
			socketErr = ErrPeer
			return
		}
		cred, socketErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil || socketErr != nil || cred == nil || cred.Pid <= 0 {
		return nil, ErrPeer
	}
	return cred, nil
}

// plainUnixReader rejects ancillary credentials/rights rather than interpreting
// them as authorization. Go's Linux ReadMsgUnix sets received FDs CLOEXEC; any
// delivered rights are closed immediately on refusal, never forwarded or used.
type plainUnixReader struct{ conn *net.UnixConn }

func (r plainUnixReader) Read(p []byte) (int, error) {
	var oob [256]byte
	n, on, flags, _, err := r.conn.ReadMsgUnix(p, oob[:])
	if on != 0 || flags&syscall.MSG_CTRUNC != 0 {
		messages, _ := syscall.ParseSocketControlMessage(oob[:on])
		for _, m := range messages {
			if rights, e := syscall.ParseUnixRights(&m); e == nil {
				for _, fd := range rights {
					syscall.Close(fd)
				}
			}
		}
		return 0, ErrProtocol
	}
	if n == 0 && (err == nil || errors.Is(err, io.EOF)) {
		return 0, io.EOF
	}
	return n, err
}

func readAuditFrame(c *net.UnixConn) ([]byte, error) {
	r := plainUnixReader{c}
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, ErrProtocol
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > MaxAuditFrame {
		return nil, ErrProtocol
	}
	body := make([]byte, int(size))
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, ErrProtocol
	}
	var tail [1]byte
	if n, err := r.Read(tail[:]); n != 0 || err != io.EOF {
		return nil, ErrProtocol
	}
	return body, nil
}

// RecordConnection owns and closes c, accepts one length-prefixed strict JSON
// object followed by the client's write-half EOF, then writes one framed reply.
// UID checks precede frame reads. Socket I/O is bounded; journal commits are not
// cancellation-safe execution permits and cannot be abandoned into a retry.
// A cancellation or lost reply can follow a committed fact. Exact replay can
// recover HISTORY ONLY. Neither acknowledgment nor Replay=false permits action.
// There is no server loop, endpoint, runtime callback or external action here.
func (r *UnixRecorder) RecordConnection(ctx context.Context, c *net.UnixConn) error {
	if c == nil {
		return ErrPeer
	}
	defer c.Close()
	if r == nil || r.journal == nil || ctx == nil {
		return ErrHistory
	}
	if ctx.Err() != nil {
		return ErrTransport
	}
	cred, err := unixCredentials(c)
	if err != nil || cred.Uid != r.uid {
		return ErrPeer
	}
	deadline := time.Now().Add(auditIOTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if c.SetDeadline(deadline) != nil {
		return ErrTransport
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	body, err := readAuditFrame(c)
	if err != nil {
		return err
	}
	request, err := parseAuditRequest(body)
	if err != nil || request.history != r.journal.HistoryID() {
		return ErrProtocol
	}
	select {
	case r.slot <- struct{}{}:
		defer func() { <-r.slot }()
	case <-ctx.Done():
		return ErrTransport
	}
	if ctx.Err() != nil {
		return ErrTransport
	}
	observation, err := r.journal.RecordClaim(request.scope, request.attempt)
	if err != nil {
		return err
	} // No reply/fact escapes an unknown commit.
	if ctx.Err() != nil {
		return ErrTransport
	}
	reply := auditResponse{AuditProtocol, "recorded_history", request.history, observation.Fact.Sequence, observation.Fact.ScopeDigest, observation.Fact.AttemptDigest, observation.Replay, Boundary, false, "none"}
	encoded, err := json.Marshal(reply)
	if err != nil || len(encoded) > MaxAuditFrame {
		return ErrTransport
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(encoded)))
	packet := append(header[:], encoded...)
	for len(packet) > 0 {
		n, e := c.Write(packet)
		if e != nil || n <= 0 {
			return ErrTransport
		}
		packet = packet[n:]
	}
	return nil
}
