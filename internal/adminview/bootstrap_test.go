package adminview

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
	"golang.org/x/crypto/ssh"
	"net/http/httptest"
	"strings"
	"testing"
)

func bootstrapFixture(t *testing.T) hostbootstrap.Request {
	key := func() ssh.PublicKey {
		_, p, _ := ed25519.GenerateKey(rand.Reader)
		s, e := ssh.NewSignerFromKey(p)
		if e != nil {
			t.Fatal(e)
		}
		return s.PublicKey()
	}
	b := make([]byte, 64)
	copy(b, []byte{127, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(b[16:], 2)
	binary.LittleEndian.PutUint16(b[18:], 62)
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint16(b[52:], 64)
	hash := sha256.Sum256(b)
	p := hostbootstrap.Plan{Address: "192.0.2.20", Port: 22, AdminUser: "bootstrap", ControllerIP: "192.0.2.10", HostKey: string(ssh.MarshalAuthorizedKey(key())), InventoryKey: string(ssh.MarshalAuthorizedKey(key())), AdminKeyFingerprint: ssh.FingerprintSHA256(key()), Architecture: "amd64", ArtifactSHA256: hex.EncodeToString(hash[:])}
	return hostbootstrap.Request{Plan: p, ReviewDigest: hostbootstrap.Digest(p), Artifact: b}
}
func TestBootstrapAPISeparateAuthorityAndExactReview(t *testing.T) {
	h, store, read := fixture(t)
	ctx := context.Background()
	if e := store.InitializeBootstrap(ctx); e != nil {
		t.Fatal(e)
	}
	r := bootstrapFixture(t)
	s, e := hostbootstrap.NewService(map[string]hostbootstrap.DeploymentTarget{"one": {Request: r, CredentialAlias: "admin"}}, store, func(context.Context, string) (ssh.Signer, error) { return nil, hostbootstrap.ErrInvalid })
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	token := strings.Repeat("e", 64)
	if h.SetBootstrap(s, read) == nil {
		t.Fatal("read role reused")
	}
	if h.SetBootstrap(s, token) != nil {
		t.Fatal("bootstrap role")
	}
	if h.EnableManagement(token) == nil {
		t.Fatal("reverse management collision")
	}
	send := func(method, body, credential, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://127.0.0.1/admin/api/v1/bootstrap", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	if send("GET", "", read, "").Code != 401 || send("GET", "", token, "https://other.invalid").Code != 403 {
		t.Fatal("authority/origin escaped")
	}
	if send("GET", "", token, "").Code != 200 {
		t.Fatal("targets unavailable")
	}
	body := map[string]any{"target_id": "one", "review_digest": r.ReviewDigest, "request_id": "11111111-1111-4111-8111-111111111111", "retry_unknown": false}
	encode := func() string { b, _ := json.Marshal(body); return string(b) }
	if send("POST", encode(), token, "").Code != 400 {
		t.Fatal("unconfirmed execution")
	}
	body["confirm_install"] = true
	body["command"] = "id"
	if send("POST", encode(), token, "").Code != 400 {
		t.Fatal("arbitrary command accepted")
	}
	delete(body, "command")
	body["review_digest"] = strings.Repeat("f", 64)
	if send("POST", encode(), token, "").Code != 409 {
		t.Fatal("changed review")
	}
	body["review_digest"] = r.ReviewDigest
	if w := send("POST", encode(), token, ""); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	s.Close()
	ops, e := store.BootstrapOperations(ctx)
	if e != nil || ops["one"].State != "unknown" {
		t.Fatal("unknown not durable", ops, e)
	}
}
