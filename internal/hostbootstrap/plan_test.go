package hostbootstrap

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"golang.org/x/crypto/ssh"
	"strings"
	"testing"
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
func sample(t *testing.T) (Request, ssh.Signer) {
	a, h, i := signer(t), signer(t), signer(t)
	b := make([]byte, 64)
	copy(b, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(b[16:], 2)
	binary.LittleEndian.PutUint16(b[18:], 62)
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint16(b[52:], 64)
	hash := sha256.Sum256(b)
	p := Plan{Address: "192.0.2.20", Port: 22, AdminUser: "bootstrap", HostKey: string(ssh.MarshalAuthorizedKey(h.PublicKey())), AdminKeyFingerprint: ssh.FingerprintSHA256(a.PublicKey()), ControllerIP: "192.0.2.10", InventoryKey: string(ssh.MarshalAuthorizedKey(i.PublicKey())), ArtifactSHA256: hex.EncodeToString(hash[:]), Architecture: "amd64"}
	return Request{Plan: p, ReviewDigest: Digest(p), Artifact: b}, a
}
func TestPlanRejectsSubstitutionAndAuthorityReuse(t *testing.T) {
	r, a := sample(t)
	if e := Validate(r); e != nil {
		t.Fatal(e)
	}
	for _, edit := range []func(*Request){func(r *Request) { r.Plan.Address = "example.com" }, func(r *Request) { r.Plan.ControllerIP = "fe80::1%eth0" }, func(r *Request) { r.Plan.AdminUser = "root;id" }, func(r *Request) { r.Plan.InventoryKey = string(ssh.MarshalAuthorizedKey(a.PublicKey())) }, func(r *Request) { r.Plan.ArtifactSHA256 = strings.Repeat("0", 64) }, func(r *Request) { r.Plan.Architecture = "s390x" }, func(r *Request) { r.Plan.HostKey = "command=\"id\" " + r.Plan.HostKey }} {
		bad := r
		edit(&bad)
		bad.ReviewDigest = Digest(bad.Plan)
		if Validate(bad) == nil {
			t.Fatal("accepted invalid request", bad.Plan)
		}
	}
	r.Plan.Port = 2222
	if Validate(r) == nil {
		t.Fatal("review substitution")
	}
}
func TestAuthorizedEntryCannotInjectOptions(t *testing.T) {
	r, _ := sample(t)
	line, e := AuthorizedKey(r.Plan)
	if e != nil || !strings.HasPrefix(string(line), `restrict,from="192.0.2.10",command="/usr/local/libexec/capacity-host-inventory" ssh-ed25519 `) {
		t.Fatal(string(line), e)
	}
	for _, s := range []string{"192.0.2.1\"\n", "::ffff:192.0.2.1", "2001:db8::1%bad"} {
		p := r.Plan
		p.ControllerIP = s
		if _, e := AuthorizedKey(p); e == nil {
			t.Fatal(s)
		}
	}
}
func TestWrongAdminCredentialFailsBeforeNetwork(t *testing.T) {
	r, _ := sample(t)
	if Execute(context.Background(), r, signer(t)) == nil {
		t.Fatal("wrong key accepted")
	}
}
func TestBoundedOutput(t *testing.T) {
	called := false
	w := &cancelWriter{cancel: func() { called = true }}
	if _, e := w.Write(make([]byte, 65537)); e == nil || !called || !w.exceeded {
		t.Fatal("unbounded output")
	}
}
