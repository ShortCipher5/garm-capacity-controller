package firstboot

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativepreflight"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

//go:embed preflight.py
var preflightScript string
var ErrPreflight = errors.New("read-only diagnostic unavailable; verify separately approved administrator credential, pinned installed diagnostic binary and protected paths")

type PreflightRecipe struct {
	Digest         string `json:"review_digest"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	ScriptSHA256   string `json:"script_sha256"`
	Mode           string `json:"mode"`
}

func preflightHash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func preflightRecipe(t DeploymentTarget) PreflightRecipe {
	h := sha256.Sum256([]byte(preflightScript))
	r := PreflightRecipe{ArtifactSHA256: t.PreflightSHA256, ScriptSHA256: hex.EncodeToString(h[:]), Mode: "read_only_unqualified"}
	b, _ := json.Marshal(struct {
		Plan   string
		Recipe PreflightRecipe
	}{t.Request.ReviewDigest, r})
	d := sha256.Sum256(b)
	r.Digest = hex.EncodeToString(d[:])
	return r
}

// Preflight uses the existing separately scoped admin authority only for a
// reviewed fixed read. It neither calls the installer nor changes durable state.
func (s *Service) Preflight(ctx context.Context, id, digest string) (nativepreflight.Report, error) {
	if ctx == nil || ctx.Err() != nil {
		return nativepreflight.Report{}, ErrPreflight
	}
	s.mu.Lock()
	t, ok := s.targets[id]
	if !ok || s.closed || s.running[id] || t.PreflightSHA256 == "" || preflightRecipe(t).Digest != digest {
		s.mu.Unlock()
		return nativepreflight.Report{}, ErrPreflight
	}
	s.running[id] = true
	s.wg.Add(1)
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.running, id); s.mu.Unlock(); s.wg.Done() }()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	key, e := s.resolve(ctx, t.CredentialAlias)
	if e != nil || key == nil {
		return nativepreflight.Report{}, ErrPreflight
	}
	return executePreflight(ctx, t, key, (&net.Dialer{Timeout: 5 * time.Second}).DialContext)
}
func executePreflight(ctx context.Context, t DeploymentTarget, key ssh.Signer, dial func(context.Context, string, string) (net.Conn, error)) (nativepreflight.Report, error) {
	if !preflightHash(t.PreflightSHA256) || Validate(t.Request) != nil || key == nil || ssh.FingerprintSHA256(key.PublicKey()) != t.Request.Plan.Base.AdminKeyFingerprint {
		return nativepreflight.Report{}, ErrPreflight
	}
	host, e := hostbootstrap.PublicKey(t.Request.Plan.Base.HostKey)
	if e != nil {
		return nativepreflight.Report{}, ErrPreflight
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	addr := net.JoinHostPort(t.Request.Plan.Base.Address, strconv.Itoa(int(t.Request.Plan.Base.Port)))
	conn, e := dial(ctx, "tcp", addr)
	if e != nil {
		return nativepreflight.Report{}, ErrPreflight
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return nativepreflight.Report{}, ErrPreflight
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
	cfg := &ssh.ClientConfig{User: t.Request.Plan.Base.AdminUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyAlgorithms: []string{ssh.KeyAlgoED25519}, HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
		if subtle.ConstantTimeCompare(host.Marshal(), k.Marshal()) != 1 {
			return ErrPreflight
		}
		return nil
	}}
	c, ch, requests, e := ssh.NewClientConn(conn, addr, cfg)
	if e != nil {
		return nativepreflight.Report{}, ErrPreflight
	}
	client := ssh.NewClient(c, ch, requests)
	defer client.Close()
	session, e := client.NewSession()
	if e != nil {
		return nativepreflight.Report{}, ErrPreflight
	}
	defer session.Close()
	nonce := make([]byte, 32)
	if _, e = rand.Read(nonce); e != nil {
		return nativepreflight.Report{}, ErrPreflight
	}
	n := hex.EncodeToString(nonce)
	raw, _ := json.Marshal(map[string]string{"sha256": t.PreflightSHA256, "nonce": n})
	session.Stdin = bytes.NewReader(raw)
	out, stderr := &limited{cancel: cancel}, &limited{cancel: cancel}
	session.Stdout = out
	session.Stderr = stderr
	command := preflightCommand()
	if session.Run(command) != nil || ctx.Err() != nil || out.exceeded || stderr.exceeded {
		return nativepreflight.Report{}, ErrPreflight
	}
	var receipt struct {
		Nonce  string                 `json:"nonce"`
		Report nativepreflight.Report `json:"report"`
	}
	d := json.NewDecoder(bytes.NewReader(out.Bytes()))
	d.DisallowUnknownFields()
	if d.Decode(&receipt) != nil || d.Decode(new(any)) != io.EOF || receipt.Nonce != n || validatePreflightReport(receipt.Report) != nil {
		return nativepreflight.Report{}, ErrPreflight
	}
	return receipt.Report, nil
}
func validatePreflightReport(r nativepreflight.Report) error {
	if r.Version != "native-host-preflight/v1" || r.State != "unqualified_no_activation" || r.ObservedAt.IsZero() || len(r.Checks) < 1 || len(r.Checks) > 64 {
		return ErrPreflight
	}
	for _, c := range r.Checks {
		if len(c.Name) < 1 || len(c.Name) > 64 || len(c.Observed) > 4096 || len(c.Required) > 4096 {
			return ErrPreflight
		}
		switch c.State {
		case "missing_or_unsupported", "observed_prerequisite_only", "observed_requirement_only", "unavailable", "informational", "not_exercised", "unqualified":
		default:
			return ErrPreflight
		}
	}
	return nil
}

func preflightCommand() string {
	return "/usr/bin/sudo -n /usr/bin/python3 -I -c '" + strings.ReplaceAll(preflightScript, "'", "'\"'\"'") + "'"
}
