package firstboot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

//go:embed rotation.py
var rotationProgram string

type RotationPlan struct {
	RequestID                string `json:"request_id"`
	Installation             Plan   `json:"installation"`
	InstallationDigest       string `json:"installation_digest"`
	ListenerHostKey          string `json:"listener_host_key"`
	OldKey                   string `json:"old_key"`
	NewKey                   string `json:"new_key"`
	ProgramSHA256            string `json:"program_sha256"`
	RestartInventorySessions bool   `json:"restart_inventory_sessions"`
}
type RotationRequest struct {
	Plan         RotationPlan `json:"plan"`
	ReviewDigest string       `json:"review_digest"`
	Artifact     []byte       `json:"artifact"`
	Phase        string       `json:"phase"`
}
type RotationReceipt struct {
	State              string `json:"state"`
	Digest             string `json:"review_digest"`
	HostKey            string `json:"host_key"`
	OldKeyRemoved      bool   `json:"old_key_removed"`
	SessionsTerminated bool   `json:"sessions_terminated"`
}

func rotationSource() string {
	b, _ := json.Marshal(installer)
	return "__name__='fixed_inventory_module'\nexec(" + string(b) + ")\n" + rotationProgram
}
func RotationProgramSHA256() string {
	h := sha256.Sum256([]byte(rotationSource()))
	return hex.EncodeToString(h[:])
}
func RotationDigest(p RotationPlan) string {
	b, _ := json.Marshal(p)
	var v any
	json.Unmarshal(b, &v)
	b, _ = json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ValidateRotation(r RotationRequest) error {
	p := r.Plan
	if !requestName.MatchString(p.RequestID) || p.ProgramSHA256 != RotationProgramSHA256() || !p.RestartInventorySessions || r.ReviewDigest != RotationDigest(p) || (r.Phase != "stage" && r.Phase != "commit") || Validate(Request{Plan: p.Installation, ReviewDigest: p.InstallationDigest, Artifact: r.Artifact}) != nil {
		return ErrInvalid
	}
	old, e := hostbootstrap.PublicKey(p.OldKey)
	if e != nil {
		return ErrInvalid
	}
	next, e := hostbootstrap.PublicKey(p.NewKey)
	if e != nil {
		return ErrInvalid
	}
	if bytes.Equal(old.Marshal(), next.Marshal()) || ssh.FingerprintSHA256(old) == p.Installation.Base.AdminKeyFingerprint || ssh.FingerprintSHA256(next) == p.Installation.Base.AdminKeyFingerprint {
		return ErrInvalid
	}
	if _, e := hostbootstrap.PublicKey(p.ListenerHostKey); e != nil {
		return ErrInvalid
	}
	return nil
}
func rotationCommand() string {
	return "/usr/bin/sudo -n /usr/bin/python3 -I -c '" + strings.ReplaceAll(rotationSource(), "'", "'\"'\"'") + "'"
}

// RotateInventoryKey stages the new public key, proves its pinned read-only access,
// then removes the old key and restarts only the isolated inventory listener.
// Any failed stage is unknown; callers must durably authorize before invoking it.
func RotateInventoryKey(ctx context.Context, r RotationRequest, admin, newKey ssh.Signer) (RotationReceipt, error) {
	return rotateInventoryKey(ctx, r, admin, newKey, rotationPhase, func(ctx context.Context, p RotationPlan, key ssh.Signer) error {
		c, e := sshinventory.New(sshinventory.Config{Address: p.Installation.InventoryAddress, Port: p.Installation.InventoryPort, User: "garm-inventory-listener", HostKey: p.ListenerHostKey, CredentialRef: "new-inventory"}, func(context.Context, string) (ssh.Signer, error) { return key, nil })
		if e != nil {
			return e
		}
		_, e = c.Probe(ctx)
		return e
	})
}
func rotateInventoryKey(ctx context.Context, r RotationRequest, admin, newKey ssh.Signer, phase func(context.Context, RotationRequest, ssh.Signer) (RotationReceipt, error), probe func(context.Context, RotationPlan, ssh.Signer) error) (RotationReceipt, error) {
	r.Phase = "stage"
	if ValidateRotation(r) != nil || admin == nil || newKey == nil || ssh.FingerprintSHA256(admin.PublicKey()) != r.Plan.Installation.Base.AdminKeyFingerprint {
		return RotationReceipt{}, ErrInvalid
	}
	expected, _ := hostbootstrap.PublicKey(r.Plan.NewKey)
	if !bytes.Equal(expected.Marshal(), newKey.PublicKey().Marshal()) {
		return RotationReceipt{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 7*time.Minute)
	defer cancel()
	if _, e := phase(ctx, r, admin); e != nil {
		return RotationReceipt{}, e
	}
	if e := probe(ctx, r.Plan, newKey); e != nil {
		return RotationReceipt{}, errors.New("rotation unknown: new key may be staged; old key has not been intentionally removed; inspect and retry exact plan")
	}
	r.Phase = "commit"
	receipt, e := phase(ctx, r, admin)
	if e != nil {
		return RotationReceipt{}, e
	}
	if e = probe(ctx, r.Plan, newKey); e != nil {
		return RotationReceipt{}, errors.New("rotation unknown: old key may be revoked and sessions terminated, but final new-key inventory probe failed")
	}
	return receipt, nil
}
func rotationPhase(ctx context.Context, r RotationRequest, key ssh.Signer) (RotationReceipt, error) {
	fail := func() (RotationReceipt, error) {
		return RotationReceipt{}, errors.New("key rotation outcome unknown or refused; inspect protected journal and retry only identical approved plan; old key may remain or listener may need recovery")
	}
	if ValidateRotation(r) != nil || key == nil || ssh.FingerprintSHA256(key.PublicKey()) != r.Plan.Installation.Base.AdminKeyFingerprint {
		return RotationReceipt{}, ErrInvalid
	}
	host, e := hostbootstrap.PublicKey(r.Plan.Installation.Base.HostKey)
	if e != nil {
		return RotationReceipt{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	address := net.JoinHostPort(r.Plan.Installation.Base.Address, strconv.Itoa(int(r.Plan.Installation.Base.Port)))
	conn, e := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
	if e != nil {
		return fail()
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return fail()
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
	cfg := &ssh.ClientConfig{User: r.Plan.Installation.Base.AdminUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyAlgorithms: []string{ssh.KeyAlgoED25519}, HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
		if subtle.ConstantTimeCompare(k.Marshal(), host.Marshal()) != 1 {
			return ErrInvalid
		}
		return nil
	}}
	c, ch, requests, e := ssh.NewClientConn(conn, address, cfg)
	if e != nil {
		return fail()
	}
	client := ssh.NewClient(c, ch, requests)
	defer client.Close()
	session, e := client.NewSession()
	if e != nil {
		return fail()
	}
	defer session.Close()
	raw, _ := json.Marshal(r)
	session.Stdin = bytes.NewReader(raw)
	out := &limited{cancel: cancel}
	stderr := &limited{cancel: cancel}
	session.Stdout = out
	session.Stderr = stderr
	if session.Run(rotationCommand()) != nil || ctx.Err() != nil || out.exceeded || stderr.exceeded {
		return fail()
	}
	var receipt RotationReceipt
	d := json.NewDecoder(bytes.NewReader(out.Bytes()))
	d.DisallowUnknownFields()
	if d.Decode(&receipt) != nil || d.Decode(new(any)) != io.EOF || validateRotationReceipt(r, receipt) != nil {
		return fail()
	}
	return receipt, nil
}

func validateRotationReceipt(r RotationRequest, v RotationReceipt) error {
	want := "staged"
	if r.Phase == "commit" {
		want = "revoked"
	}
	if v.State != want || v.Digest != r.ReviewDigest || strings.TrimSpace(v.HostKey) != strings.TrimSpace(r.Plan.ListenerHostKey) || v.OldKeyRemoved != (r.Phase == "commit") || v.SessionsTerminated != (r.Phase == "commit") {
		return ErrInvalid
	}
	return nil
}
