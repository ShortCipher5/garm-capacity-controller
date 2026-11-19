//go:build linux

package nativeprovision

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativepreflight"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprepare"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

//go:embed diagnostic.py
var diagnosticScript string
var ErrDiagnostic = errors.New("prepared diagnostic unavailable; verify exact completed preparation, pins and existing SSH prerequisites")

type DiagnosticRecipe struct {
	ReviewDigest   string `json:"review_digest"`
	PreparationID  string `json:"preparation_id"`
	ManifestDigest string `json:"manifest_digest"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	ScriptSHA256   string `json:"script_sha256"`
	Mode           string `json:"mode"`
}

func (s *Service) diagnosticRecipe() DiagnosticRecipe {
	md, _ := nativeprepare.ManifestDigest(s.plan.Manifest)
	r := DiagnosticRecipe{PreparationID: s.plan.Manifest.ID, ManifestDigest: md, ArtifactSHA256: s.plan.Manifest.Files["capacity-native-preflight"], ScriptSHA256: digest([]byte(diagnosticScript)), Mode: "read_only_unqualified"}
	raw, _ := json.Marshal(struct {
		Plan   string
		Recipe DiagnosticRecipe
	}{s.ReviewDigest(), r})
	r.ReviewDigest = digest(raw)
	return r
}

// Diagnostic is an explicitly approved read. It never writes an attempt or
// changes preparation history. The shared lock excludes prepare/replacement;
// shutdown cancellation joins before bundle handles are closed.
func (s *Service) Diagnostic(ctx context.Context, id, review string, resolve func() (ssh.Signer, error)) (nativepreflight.Report, error) {
	return s.diagnostic(ctx, id, review, resolve, (&net.Dialer{Timeout: 5 * time.Second}).DialContext)
}
func (s *Service) diagnostic(ctx context.Context, id, review string, resolve func() (ssh.Signer, error), dial func(context.Context, string, string) (net.Conn, error)) (nativepreflight.Report, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || resolve == nil || s.closed.Load() || id != s.plan.Manifest.ID || review != s.diagnosticRecipe().ReviewDigest || !s.busy.CompareAndSwap(false, true) {
		return nativepreflight.Report{}, ErrDiagnostic
	}
	defer s.busy.Store(false)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.Load() {
		return nativepreflight.Report{}, ErrDiagnostic
	}
	lock, e := s.lock()
	if e != nil {
		return nativepreflight.Report{}, ErrDiagnostic
	}
	defer lock.Close()
	state, e := s.Status()
	if e != nil || state.Receipt == nil || state.Status != "prepared_not_activated" {
		return nativepreflight.Report{}, ErrDiagnostic
	}
	work, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stop := context.AfterFunc(s.life, cancel)
	defer stop()
	key, e := resolve()
	if e != nil {
		return nativepreflight.Report{}, ErrDiagnostic
	}
	return executeDiagnostic(work, s.plan, *state.Receipt, key, dial)
}
func executeDiagnostic(ctx context.Context, p Plan, receipt nativeprepare.Receipt, key ssh.Signer, dial func(context.Context, string, string) (net.Conn, error)) (nativepreflight.Report, error) {
	if validate(p) != nil || nativeprepare.ValidateReceipt(p.Manifest, receipt) != nil || key == nil || ssh.FingerprintSHA256(key.PublicKey()) != p.Target.AdminKeyFingerprint {
		return nativepreflight.Report{}, ErrDiagnostic
	}
	host, e := hostbootstrap.PublicKey(p.Target.HostKey)
	if e != nil {
		return nativepreflight.Report{}, ErrDiagnostic
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	addr := net.JoinHostPort(p.Target.Address, strconv.Itoa(int(p.Target.Port)))
	conn, e := dial(ctx, "tcp", addr)
	if e != nil {
		return nativepreflight.Report{}, ErrDiagnostic
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return nativepreflight.Report{}, ErrDiagnostic
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
	cfg := &ssh.ClientConfig{User: p.Target.AdminUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyAlgorithms: []string{ssh.KeyAlgoED25519}, HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
		if subtle.ConstantTimeCompare(host.Marshal(), k.Marshal()) != 1 {
			return ErrDiagnostic
		}
		return nil
	}}
	c, ch, requests, e := ssh.NewClientConn(conn, addr, cfg)
	if e != nil {
		return nativepreflight.Report{}, ErrDiagnostic
	}
	client := ssh.NewClient(c, ch, requests)
	defer client.Close()
	session, e := client.NewSession()
	if e != nil {
		return nativepreflight.Report{}, ErrDiagnostic
	}
	defer session.Close()
	nonce := make([]byte, 32)
	if _, e = rand.Read(nonce); e != nil {
		return nativepreflight.Report{}, ErrDiagnostic
	}
	n := hex.EncodeToString(nonce)
	raw, _ := json.Marshal(map[string]any{"sha256": p.Manifest.Files["capacity-native-preflight"], "nonce": n, "receipt": receipt})
	session.Stdin = bytes.NewReader(raw)
	out, stderr := &boundedOutput{max: 65536}, &boundedOutput{max: 65536}
	session.Stdout = out
	session.Stderr = stderr
	command := diagnosticCommand()
	if session.Run(command) != nil || ctx.Err() != nil {
		return nativepreflight.Report{}, ErrDiagnostic
	}
	var response struct {
		Nonce  string                 `json:"nonce"`
		Report nativepreflight.Report `json:"report"`
	}
	d := json.NewDecoder(bytes.NewReader(out.Bytes()))
	d.DisallowUnknownFields()
	if d.Decode(&response) != nil || d.Decode(new(any)) != io.EOF || response.Nonce != n || validatePreflightReport(response.Report) != nil {
		return nativepreflight.Report{}, ErrDiagnostic
	}
	return response.Report, nil
}
func validatePreflightReport(r nativepreflight.Report) error {
	if r.Version != "native-host-preflight/v1" || r.State != "unqualified_no_activation" || r.ObservedAt.IsZero() || len(r.Checks) < 1 || len(r.Checks) > 64 {
		return ErrDiagnostic
	}
	for _, c := range r.Checks {
		if len(c.Name) < 1 || len(c.Name) > 64 || len(c.Observed) > 4096 || len(c.Required) > 4096 {
			return ErrDiagnostic
		}
		switch c.State {
		case "missing_or_unsupported", "observed_prerequisite_only", "observed_requirement_only", "unavailable", "informational", "not_exercised", "unqualified":
		default:
			return ErrDiagnostic
		}
	}
	return nil
}

func diagnosticCommand() string {
	return "/usr/bin/sudo -n /usr/bin/python3 -I -c '" + strings.ReplaceAll(diagnosticScript, "'", "'\"'\"'") + "'"
}
