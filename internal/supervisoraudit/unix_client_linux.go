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
	"time"
)

var ErrAcknowledgment = errors.New("supervisor audit: unconfirmed history acknowledgment; no action authority")

// UnixHistoryReceipt is an immutable observation of a matching server response,
// NOT proof that the server durably recorded it. Kernel connect-time UID/PID do
// not attest enrollment, current FD holder, process birth, code or isolation.
// Private fields prevent constructing an observed value from raw JSON. Every
// value, including zero and replay, permanently denies action authority.
type UnixHistoryReceipt struct {
	observed  bool
	history   string
	fact      Fact
	replay    bool
	serverUID uint32
	serverPID int32
}

func (r UnixHistoryReceipt) ObservedHistory() bool       { return r.observed }
func (r UnixHistoryReceipt) HistoryID() string           { return r.history }
func (r UnixHistoryReceipt) Fact() Fact                  { return r.fact }
func (r UnixHistoryReceipt) Replay() bool                { return r.replay }
func (r UnixHistoryReceipt) ServerConnectionUID() uint32 { return r.serverUID }
func (r UnixHistoryReceipt) ServerConnectionPID() int32  { return r.serverPID }
func (r UnixHistoryReceipt) Boundary() string            { return Boundary }
func (r UnixHistoryReceipt) ActivationAuthorized() bool  { return false }
func (r UnixHistoryReceipt) ExecutionAuthority() string  { return "none" }

func parseAuditAcknowledgment(body []byte, request auditRequest) (auditResponse, error) {
	var a auditResponse
	fields := map[string]any{"protocol": &a.Protocol, "outcome": &a.Outcome, "history_id": &a.History, "sequence": &a.Sequence, "scope_digest": &a.Scope, "attempt_digest": &a.Attempt, "replay": &a.Replay, "boundary": &a.Boundary, "activation_authorized": &a.ActivationAuthorized, "execution_authority": &a.ExecutionAuthority}
	d := json.NewDecoder(bytes.NewReader(body))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return auditResponse{}, ErrAcknowledgment
	}
	seen := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		target, known := fields[key]
		if err != nil || !ok || !known || seen[key] {
			return auditResponse{}, ErrAcknowledgment
		}
		seen[key] = true
		var raw json.RawMessage
		if d.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, target) != nil {
			return auditResponse{}, ErrAcknowledgment
		}
	}
	token, err = d.Token()
	var extra any
	if err != nil || token != json.Delim('}') || d.Decode(&extra) != io.EOF || len(seen) != len(fields) || a.Protocol != AuditProtocol || a.Outcome != "recorded_history" || a.History != request.history || a.Scope != request.scope || a.Attempt != request.attempt || a.Sequence <= 0 || a.Sequence > MaxFacts || a.Boundary != Boundary || a.ActivationAuthorized || a.ExecutionAuthority != "none" {
		return auditResponse{}, ErrAcknowledgment
	}
	return a, nil
}

// ObserveUnixClaim owns/closes an already connected Unix stream. It checks the
// actual server connect-time UID BEFORE sending any request, sends once, and
// accepts only an exact request-bound, explicit no-authority acknowledgment.
// There is no dialing, automatic retry/reconnect, launch, runtime or release.
//
// The caller must protect/select the endpoint and UID independently. A matching
// same-UID or FD-delegated server can fabricate a valid response with no journal;
// this API intentionally makes NO externally verified commit/identity claim.
// Failed/partial/malformed/lost acknowledgments return only a zero receipt even
// if recording committed. An exact later retry recovers history, never execution.
func ObserveUnixClaim(ctx context.Context, c *net.UnixConn, allowedServerUID uint32, history, scope, attempt string) (UnixHistoryReceipt, error) {
	var zero UnixHistoryReceipt
	if c == nil {
		return zero, ErrPeer
	}
	defer c.Close()
	if ctx == nil || !digest(history) || !digest(scope) || !digest(attempt) {
		return zero, ErrProtocol
	}
	if ctx.Err() != nil {
		return zero, ErrTransport
	}
	cred, err := unixCredentials(c)
	if err != nil || cred.Uid != allowedServerUID {
		return zero, ErrPeer
	}
	deadline := time.Now().Add(auditIOTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if c.SetDeadline(deadline) != nil {
		return zero, ErrTransport
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	request := auditRequest{AuditProtocol, "record_claim", history, scope, attempt}
	encoded, err := json.Marshal(map[string]string{"protocol": request.protocol, "command": request.command, "history_id": history, "scope_digest": scope, "attempt_digest": attempt})
	if err != nil || len(encoded) > MaxAuditFrame {
		return zero, ErrProtocol
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(encoded)))
	packet := append(header[:], encoded...)
	for len(packet) > 0 {
		n, e := c.Write(packet)
		if e != nil || n <= 0 {
			return zero, ErrTransport
		}
		packet = packet[n:]
	}
	if c.CloseWrite() != nil {
		return zero, ErrTransport
	}
	body, err := readAuditFrame(c)
	if err != nil {
		return zero, ErrAcknowledgment
	}
	a, err := parseAuditAcknowledgment(body, request)
	if err != nil {
		return zero, err
	}
	if ctx.Err() != nil {
		return zero, ErrTransport
	}
	return UnixHistoryReceipt{true, a.History, Fact{a.Sequence, a.Scope, a.Attempt}, a.Replay, cred.Uid, cred.Pid}, nil
}
