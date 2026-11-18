//go:build linux

package nativeprovision

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprepare"
	"golang.org/x/crypto/ssh"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Bundle, Target) {
	t.Helper()
	dir := t.TempDir()
	m := nativeprepare.Manifest{Version: nativeprepare.Version, ID: strings.Repeat("a", 64), Files: map[string]string{}}
	for _, n := range nativeprepare.FileNames() {
		data := []byte(n)
		m.Files[n] = digest(data)
		if e := os.WriteFile(filepath.Join(dir, n), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	raw, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600)
	b, e := loadBundle(dir, uint32(os.Geteuid()), false)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { b.Close() })
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewSignerFromKey(priv)
	return b, Target{Address: "192.0.2.1", Port: 22, AdminUser: "administrator", HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key.PublicKey()))), AdminKeyFingerprint: ssh.FingerprintSHA256(key.PublicKey())}
}
func TestPlanIsOwnedAndClosedBundleRefused(t *testing.T) {
	b, target := fixture(t)
	p, e := b.Plan(target)
	if e != nil {
		t.Fatal(e)
	}
	p.Manifest.Files["capsule.json"] = "bad"
	p.Files[0].Name = "bad"
	next, e := b.Plan(target)
	if e != nil || validate(next) != nil {
		t.Fatal("caller mutated owned plan", e)
	}
	b.Close()
	if _, e = b.Plan(target); e == nil {
		t.Fatal("closed")
	}
}
func TestWrongDigestNeverDials(t *testing.T) {
	b, target := fixture(t)
	p, _ := b.Plan(target)
	_, e := b.Execute(context.Background(), p, "bad", nil, false)
	if e != ErrInvalid {
		t.Fatal(e)
	}
}
func TestServiceConsumesBeforeResolverAndNeverReprepares(t *testing.T) {
	b, target := fixture(t)
	p, _ := b.Plan(target)
	dir := t.TempDir()
	s := &Service{bundle: b, plan: p, dir: dir, uid: uint32(os.Geteuid()), execute: b.Execute}
	calls := 0
	resolver := func() (ssh.Signer, error) {
		calls++
		raw, e := os.ReadFile(filepath.Join(dir, p.Manifest.ID+".intent.json"))
		if e != nil || !strings.Contains(string(raw), "unknown") {
			t.Fatal("not durable before resolver")
		}
		return nil, ErrInvalid
	}
	state, e := s.Apply(context.Background(), Digest(p), false, resolver)
	if e != ErrUnknown || state.Status != "unknown" || calls != 1 {
		t.Fatal(state, e, calls)
	}
	_, e = s.Apply(context.Background(), Digest(p), false, resolver)
	if e != ErrUnknown || calls != 1 {
		t.Fatal("automatic reupload", e, calls)
	}
	_, e = s.Apply(context.Background(), Digest(p), true, resolver)
	if e != ErrUnknown || calls != 2 {
		t.Fatal("explicit inspect", e, calls)
	}
}
func TestChangedPlanCannotAdoptJournal(t *testing.T) {
	b, target := fixture(t)
	p, _ := b.Plan(target)
	dir := t.TempDir()
	s := &Service{bundle: b, plan: p, dir: dir, uid: uint32(os.Geteuid()), execute: b.Execute}
	resolver := func() (ssh.Signer, error) { return nil, ErrInvalid }
	s.Apply(context.Background(), Digest(p), false, resolver)
	s.plan.Target.Port = 23
	_, e := s.Apply(context.Background(), s.ReviewDigest(), true, resolver)
	if e != ErrInvalid {
		t.Fatal(e)
	}
}
func TestInspectCannotCreateIntent(t *testing.T) {
	b, target := fixture(t)
	p, _ := b.Plan(target)
	dir := t.TempDir()
	s := &Service{bundle: b, plan: p, dir: dir, uid: uint32(os.Geteuid()), execute: b.Execute}
	_, e := s.Apply(context.Background(), Digest(p), true, func() (ssh.Signer, error) { t.Fatal("resolver"); return nil, nil })
	if e != ErrInvalid {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(dir, p.Manifest.ID+".intent.json")); !os.IsNotExist(e) {
		t.Fatal("created")
	}
}
func TestJournalFIFORejectedWithoutBlocking(t *testing.T) {
	b, target := fixture(t)
	p, _ := b.Plan(target)
	dir := t.TempDir()
	s := &Service{bundle: b, plan: p, dir: dir, uid: uint32(os.Geteuid()), execute: b.Execute}
	if e := syscall.Mkfifo(filepath.Join(dir, p.Manifest.ID+".intent.json"), 0600); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := s.Status(); done <- e }()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO blocked")
	}
}
func TestStartReturnsDurableUnknownBeforeNetwork(t *testing.T) {
	b, target := fixture(t)
	p, _ := b.Plan(target)
	dir := t.TempDir()
	s := &Service{bundle: b, plan: p, dir: dir, uid: uint32(os.Geteuid()), execute: b.Execute}
	wait := make(chan struct{})
	done := make(chan struct{})
	resolver := func() (ssh.Signer, error) { defer close(done); <-wait; return nil, ErrInvalid }
	state, e := s.Start(context.Background(), Digest(p), false, resolver)
	if e != nil || state.Status != "unknown" {
		t.Fatal(state, e)
	}
	actual, e := s.Status()
	if e != nil || actual.Status != "unknown" {
		t.Fatal(actual, e)
	}
	if _, e = s.Start(context.Background(), Digest(p), true, resolver); e == nil {
		t.Fatal("concurrent accepted")
	}
	close(wait)
	<-done
}
func TestRejectedStartReleasesBusyBeforeReturn(t *testing.T) {
	b, target := fixture(t)
	p, _ := b.Plan(target)
	s := &Service{bundle: b, plan: p, dir: t.TempDir(), uid: uint32(os.Geteuid()), execute: b.Execute}
	defer s.Close()
	resolver := func() (ssh.Signer, error) { return nil, ErrInvalid }
	for i := 0; i < 100; i++ {
		if _, e := s.Start(context.Background(), "invalid", false, resolver); e != ErrInvalid || s.busy.Load() {
			t.Fatal("invalid review retained busy", e)
		}
		if _, e := s.Start(context.Background(), Digest(p), true, resolver); e != ErrInvalid || s.busy.Load() {
			t.Fatal("failed inspect retained busy", e)
		}
	}
	state, e := s.Start(context.Background(), Digest(p), false, resolver)
	if e != nil || state.Status != "unknown" {
		t.Fatal("valid start rejected after failed requests", state, e)
	}
}
