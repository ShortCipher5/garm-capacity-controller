package hostbootstrap

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
	"net"
	"strconv"
	"time"
)

// Execute never uses ssh-agent, ambient configuration, passwords, a PTY or
// caller-provided commands. Once a session starts, every error is uncertain:
// reconnect with the identical reviewed plan, never assume rollback.
func Execute(ctx context.Context, r Request, admin ssh.Signer) error {
	return execute(ctx, r, admin, (&net.Dialer{Timeout: 5 * time.Second}).DialContext)
}
func execute(ctx context.Context, r Request, admin ssh.Signer, dial func(context.Context, string, string) (net.Conn, error)) error {
	if e := Validate(r); e != nil {
		return e
	}
	if admin == nil || ssh.FingerprintSHA256(admin.PublicKey()) != r.Plan.AdminKeyFingerprint {
		return ErrInvalid
	}
	host, e := PublicKey(r.Plan.HostKey)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	addr := net.JoinHostPort(r.Plan.Address, strconv.Itoa(int(r.Plan.Port)))
	conn, e := dial(ctx, "tcp", addr)
	if e != nil {
		return errors.New("bootstrap connection unavailable; no session started")
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return errors.New("bootstrap deadline unavailable")
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	cfg := &ssh.ClientConfig{User: r.Plan.AdminUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(admin)}, HostKeyAlgorithms: []string{ssh.KeyAlgoED25519}, HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
		if subtle.ConstantTimeCompare(k.Marshal(), host.Marshal()) != 1 {
			return ErrInvalid
		}
		return nil
	}}
	c, ch, req, e := ssh.NewClientConn(conn, addr, cfg)
	if e != nil {
		return errors.New("pinned administrator authentication failed; no session started")
	}
	client := ssh.NewClient(c, ch, req)
	defer client.Close()
	session, e := client.NewSession()
	if e != nil {
		return errors.New("bootstrap session unavailable")
	}
	defer session.Close()
	body, e := json.Marshal(r)
	if e != nil {
		return ErrInvalid
	}
	session.Stdin = bytes.NewReader(body)
	out := &cancelWriter{cancel: cancel}
	errout := &cancelWriter{cancel: cancel}
	session.Stdout = out
	session.Stderr = errout
	if session.Run(Command) != nil || ctx.Err() != nil || out.exceeded || errout.exceeded {
		return fmt.Errorf("bootstrap outcome uncertain or refused; inspect protected host intent and retry only identical plan %s", r.ReviewDigest)
	}
	if out.String() != "inventory-bootstrap-installed "+r.ReviewDigest+"\n" {
		return errors.New("bootstrap response mismatch; installation outcome uncertain")
	}
	return nil
}

type cancelWriter struct {
	limitWriter
	cancel context.CancelFunc
}

func (w *cancelWriter) Write(p []byte) (int, error) {
	n, e := w.limitWriter.Write(p)
	if e != nil {
		w.cancel()
	}
	return n, e
}
