//go:build linux

package nativeassettransfer

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassets"
	"golang.org/x/crypto/ssh"
)

func signer(t *testing.T) ssh.Signer {
	t.Helper()
	_, k, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	s, e := ssh.NewSignerFromKey(k)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func protectedTemp(t *testing.T) string {
	t.Helper()
	p, e := os.MkdirTemp(".", ".assets-transfer-")
	if e != nil {
		t.Fatal(e)
	}
	p, e = filepath.Abs(p)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(p) })
	return p
}
func fixture(t *testing.T) (*Bundle, Target, ssh.Signer, string) {
	t.Helper()
	dir := protectedTemp(t)
	bundle := filepath.Join(dir, "bundle")
	os.Mkdir(bundle, 0700)
	m := nativeassets.Manifest{Version: nativeassets.Version, ID: strings.Repeat("a", 64), Architecture: "amd64", Files: map[string]nativeassets.Entry{}}
	for _, n := range nativeassets.Names() {
		path := filepath.Join(bundle, n)
		if n == "runtime.raw" {
			f, e := os.Create(path)
			if e != nil {
				t.Fatal(e)
			}
			f.Truncate(64 << 20)
			f.Close()
		} else {
			os.WriteFile(path, []byte(n), 0600)
		}
		f, _ := os.Open(path)
		h := sha256.New()
		size, e := io.Copy(h, f)
		f.Close()
		if e != nil {
			t.Fatal(e)
		}
		m.Files[n] = nativeassets.Entry{SHA256: hex.EncodeToString(h.Sum(nil)), Size: size}
	}
	raw, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(bundle, "manifest.json"), raw, 0600)
	helper := filepath.Join(dir, "helper")
	os.WriteFile(helper, []byte("pinned helper test bytes, never executed"), 0600)
	b, e := LoadBundle(bundle, helper, digest(raw), digest([]byte("pinned helper test bytes, never executed")))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { b.Close() })
	a, h := signer(t), signer(t)
	target := Target{Address: "192.0.2.1", Port: 22, AdminUser: "admin", AdminKeyFingerprint: ssh.FingerprintSHA256(a.PublicKey()), HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(h.PublicKey())))}
	journal := filepath.Join(dir, "journal")
	os.Mkdir(journal, 0700)
	return b, target, a, journal
}

// Real disposable SSH server validates every frame; it never runs the privileged
// Python recipe or any artifact. This is transport qualification, not host setup.
func server(t *testing.T, b *Bundle, target *Target, admin ssh.Signer, mode string, pinned ...ssh.Signer) (func(context.Context, string, string) (net.Conn, error), <-chan error) {
	t.Helper()
	host := signer(t)
	if len(pinned) > 0 {
		host = pinned[0]
	}
	target.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(host.PublicKey())))
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { l.Close() })
	done := make(chan error, 1)
	go func() {
		conn, e := l.Accept()
		if e != nil {
			done <- e
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(15 * time.Second))
		cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if ssh.FingerprintSHA256(k) != ssh.FingerprintSHA256(admin.PublicKey()) {
				return nil, ErrInvalid
			}
			return nil, nil
		}}
		cfg.AddHostKey(host)
		cc, chs, reqs, e := ssh.NewServerConn(conn, cfg)
		if e != nil {
			done <- e
			return
		}
		defer cc.Close()
		go ssh.DiscardRequests(reqs)
		in, ok := <-chs
		if !ok {
			done <- ErrInvalid
			return
		}
		ch, rqs, e := in.Accept()
		if e != nil {
			done <- e
			return
		}
		defer ch.Close()
		req := <-rqs
		var cmd struct{ Command string }
		if req == nil || req.Type != "exec" || ssh.Unmarshal(req.Payload, &cmd) != nil || cmd.Command != command() {
			done <- ErrInvalid
			return
		}
		req.Reply(true, nil)
		r := bufio.NewReader(ch)
		raw, e := r.ReadBytes('\n')
		if e != nil {
			done <- e
			return
		}
		var h struct {
			Mode           string                `json:"mode"`
			Manifest       nativeassets.Manifest `json:"manifest"`
			ManifestJSON   string                `json:"manifest_json"`
			ManifestDigest string                `json:"manifest_digest"`
			Files          []File                `json:"files"`
		}
		if json.Unmarshal(raw, &h) != nil || h.ManifestJSON != canonicalManifest(b.manifest) || h.ManifestDigest != manifestDigest(b.manifest) {
			done <- ErrInvalid
			return
		}
		if h.Mode == "prepare" {
			for _, f := range b.files {
				hash := sha256.New()
				n, e := io.CopyN(hash, r, f.Size)
				if e != nil || n != f.Size || hex.EncodeToString(hash.Sum(nil)) != f.SHA256 {
					done <- ErrInvalid
					return
				}
			}
		} else if h.Mode != "inspect" {
			done <- ErrInvalid
			return
		}
		tail, e := io.ReadAll(r)
		if e != nil || len(tail) != 0 {
			done <- ErrInvalid
			return
		}
		if mode == "lost" {
			done <- nil
			return
		}
		if mode == "block" {
			<-time.After(time.Second)
			done <- nil
			return
		}
		receipt := nativeassets.Receipt{Version: nativeassets.Version, ID: b.manifest.ID, ManifestDigest: manifestDigest(b.manifest), State: "artifacts_prepared_not_activated"}
		if mode == "wrong" {
			receipt.ActivationAuthorized = true
		}
		raw, _ = json.Marshal(receipt)
		ch.Write(raw)
		ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		done <- nil
	}()
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "192.0.2.1:22" {
			return nil, ErrInvalid
		}
		return (&net.Dialer{}).DialContext(ctx, network, l.Addr().String())
	}, done
}
func TestSSHFramesPinReceiptAndInspect(t *testing.T) {
	for _, mode := range []string{"prepare", "inspect", "wrong", "lost", "wrong_pin"} {
		t.Run(mode, func(t *testing.T) {
			b, target, admin, _ := fixture(t)
			dial, done := server(t, b, &target, admin, mode)
			if mode == "wrong_pin" {
				target.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer(t).PublicKey())))
			}
			p, e := b.Plan(target)
			if e != nil {
				t.Fatal(e)
			}
			r, e := b.execute(context.Background(), p, Digest(p), admin, mode == "inspect", dial)
			se := <-done
			if mode == "wrong" || mode == "lost" || mode == "wrong_pin" {
				if e == nil {
					t.Fatal("accepted unsafe outcome")
				}
			} else if e != nil || se != nil || r.ActivationAuthorized {
				t.Fatal(e, se, r)
			}
		})
	}
}
func TestDurableIntentConcurrentNoReplayAndReadOnlyRecovery(t *testing.T) {
	b, target, admin, journal := fixture(t)
	p, _ := b.Plan(target)
	var calls atomic.Int32
	dial := func(context.Context, string, string) (net.Conn, error) { calls.Add(1); return nil, errors.New("lost") }
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); b.runDial(context.Background(), p, Digest(p), admin, journal, false, dial) }()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate dispatch", calls.Load())
	}
	// Reconstructing a bundle cannot erase durable intent; no reconnect occurs.
	if _, e := b.runDial(context.Background(), p, Digest(p), admin, journal, false, dial); e == nil || calls.Load() != 1 {
		t.Fatal("replayed")
	}
	raw, e := os.ReadFile(filepath.Join(journal, p.Manifest.ID+".intent.json"))
	if e != nil {
		t.Fatal(e)
	}
	// Read-only recovery uses the same approved tuple; unknown stays unknown.
	if _, e := b.runDial(context.Background(), p, Digest(p), admin, journal, true, dial); e == nil || calls.Load() != 2 {
		t.Fatal("inspect contract")
	}
	after, _ := os.ReadFile(filepath.Join(journal, p.Manifest.ID+".intent.json"))
	if string(after) != string(raw) {
		t.Fatal("inspection mutated intent")
	}
	p.Target.Port = 23
	if _, e := b.runDial(context.Background(), p, Digest(p), admin, journal, true, dial); e == nil || calls.Load() != 2 {
		t.Fatal("changed target adopted")
	}
}
func TestReviewAndCancellationBeforeEffects(t *testing.T) {
	b, target, key, journal := fixture(t)
	p, _ := b.Plan(target)
	var calls atomic.Int32
	dial := func(context.Context, string, string) (net.Conn, error) { calls.Add(1); return nil, ErrUnknown }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, v := range []struct {
		ctx    context.Context
		review string
	}{{ctx, Digest(p)}, {context.Background(), strings.Repeat("0", 64)}} {
		if _, e := b.runDial(v.ctx, p, v.review, key, journal, false, dial); e == nil {
			t.Fatal("accepted")
		}
	}
	entries, _ := os.ReadDir(journal)
	if len(entries) != 0 || calls.Load() != 0 {
		t.Fatal("effects before review")
	}
}

func TestLostAcknowledgmentRestartRecoversByReadOnlyInspect(t *testing.T) {
	b, target, key, journal := fixture(t)
	host := signer(t)
	dial, done := server(t, b, &target, key, "lost", host)
	p, _ := b.Plan(target)
	if _, e := b.runDial(context.Background(), p, Digest(p), key, journal, false, dial); e == nil {
		t.Fatal("lost response accepted")
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	// A separate process-equivalent bundle object still uses the same held bytes
	// and durable journal. No new upload is permitted after the lost response.
	restored := &Bundle{manifest: b.manifest, files: append([]File(nil), b.files...), handles: b.handles}
	forbidden := func(context.Context, string, string) (net.Conn, error) {
		t.Error("replayed upload")
		return nil, ErrInvalid
	}
	if _, e := restored.runDial(context.Background(), p, Digest(p), key, journal, false, forbidden); e == nil {
		t.Fatal("replay accepted")
	}
	dial, done = server(t, restored, &target, key, "inspect", host)
	r, e := restored.runDial(context.Background(), p, Digest(p), key, journal, true, dial)
	if e != nil || r.ActivationAuthorized {
		t.Fatal(e, r)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}
func TestContextCancellationRetainsIntent(t *testing.T) {
	b, target, key, journal := fixture(t)
	var calls atomic.Int32
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		calls.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	p, _ := b.Plan(target)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, e := b.runDial(ctx, p, Digest(p), key, journal, false, dial); e == nil {
		t.Fatal("canceled accepted")
	}
	if _, e := b.runDial(context.Background(), p, Digest(p), key, journal, false, dial); e == nil || calls.Load() != 1 {
		t.Fatal("replayed canceled attempt")
	}
}

func TestBundleRejectsUnreviewedMutableAndSpecialSources(t *testing.T) {
	b, _, _, _ := fixture(t)
	image := b.handles["runtime.raw"].Name()
	directory := filepath.Dir(image)
	helper := b.handles["capacity-native-assets"].Name()
	md := manifestDigest(b.manifest)
	hd := b.files[0].SHA256
	// Wrong expected helper and manifest pins are rejected before network exists.
	for _, pins := range [][2]string{{strings.Repeat("0", 64), hd}, {md, strings.Repeat("0", 64)}} {
		if q, e := LoadBundle(directory, helper, pins[0], pins[1]); e == nil {
			q.Close()
			t.Fatal("unapproved bytes")
		}
	}
	n := filepath.Join(directory, "ip")
	original, e := os.ReadFile(n)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(n); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(helper, n); e != nil {
		t.Fatal(e)
	}
	if q, e := LoadBundle(directory, helper, md, hd); e == nil {
		q.Close()
		t.Fatal("symlink")
	}
	os.Remove(n)
	if e = os.Link(helper, n); e != nil {
		t.Fatal(e)
	}
	if q, e := LoadBundle(directory, helper, md, hd); e == nil {
		q.Close()
		t.Fatal("hardlink")
	}
	os.Remove(n)
	os.WriteFile(n, original, 0600)
	raw, _ := json.Marshal(b.manifest)
	// Duplicate JSON fields cannot alter the canonical review interpretation.
	raw = append(raw[:len(raw)-1], []byte(",\"id\":\""+b.manifest.ID+"\"}")...)
	os.WriteFile(filepath.Join(directory, "manifest.json"), raw, 0600)
	if q, e := LoadBundle(directory, helper, digest(raw), hd); e == nil {
		q.Close()
		t.Fatal("noncanonical manifest")
	}
}
