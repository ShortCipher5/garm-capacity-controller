package adminview

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprepare"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"golang.org/x/crypto/ssh"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func preparationFixture(t *testing.T) *nativeprovision.Service {
	t.Helper()
	cwd, _ := os.Getwd()
	dir, e := os.MkdirTemp(cwd, ".preparation-test-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	bundleDir := filepath.Join(dir, "bundle")
	journal := filepath.Join(dir, "journal")
	os.Mkdir(bundleDir, 0700)
	os.Mkdir(journal, 0700)
	m := nativeprepare.Manifest{Version: nativeprepare.Version, ID: strings.Repeat("a", 64), Files: map[string]string{}}
	for _, n := range nativeprepare.FileNames() {
		v := []byte(n)
		h := sha256.Sum256(v)
		m.Files[n] = hex.EncodeToString(h[:])
		os.WriteFile(filepath.Join(bundleDir, n), v, 0600)
	}
	raw, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(bundleDir, "manifest.json"), raw, 0600)
	b, e := nativeprovision.LoadBundle(bundleDir)
	if e != nil {
		t.Fatal(e)
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewSignerFromKey(priv)
	s, e := nativeprovision.NewService(b, nativeprovision.Target{Address: "192.0.2.1", Port: 22, AdminUser: "administrator", HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key.PublicKey()))), AdminKeyFingerprint: ssh.FingerprintSHA256(key.PublicKey())}, journal)
	if e != nil {
		b.Close()
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return s
}
func TestNativePreparationSeparateAuthorityAndDurableUnknown(t *testing.T) {
	h, _, read := fixture(t)
	s := preparationFixture(t)
	wait := make(chan struct{})
	defer close(wait) // Release resolver before the fixture joins Service.Close.
	targets := map[string]NativePreparationTarget{"one": {Service: s, Resolve: func() (ssh.Signer, error) { <-wait; return nil, nativeprovision.ErrInvalid }}}
	token := strings.Repeat("b", 64)
	if h.SetNativePreparation(targets, read) == nil {
		t.Fatal("read token reused")
	}
	if e := h.SetNativePreparation(targets, token); e != nil {
		t.Fatal(e)
	}
	if h.EnableManagement(token) == nil {
		t.Fatal("reverse role collision")
	}
	send := func(method, body, credential, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://127.0.0.1/admin/api/v1/native-preparation", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+credential)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if send("GET", "", read, "").Code != 401 || send("GET", "", token, "https://other.invalid").Code != 403 {
		t.Fatal("auth/origin")
	}
	w := send("GET", "", token, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "PRIVATE KEY") || !strings.Contains(w.Body.String(), "host_key_fingerprint") {
		t.Fatal(w.Code, w.Body.String())
	}
	body := map[string]any{"target_id": "one", "review_digest": s.ReviewDigest(), "inspect_only": false}
	encode := func() string { v, _ := json.Marshal(body); return string(v) }
	if send("POST", encode(), token, "").Code != 400 {
		t.Fatal("missing approval")
	}
	body["confirm_prepare"] = true
	body["command"] = "id"
	if send("POST", encode(), token, "").Code != 400 {
		t.Fatal("command")
	}
	delete(body, "command")
	body["review_digest"] = strings.Repeat("f", 64)
	if send("POST", encode(), token, "").Code != 409 {
		t.Fatal("drift")
	}
	body["review_digest"] = s.ReviewDigest()
	if w = send("POST", encode(), token, ""); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	if state, e := s.Status(); e != nil || state.Status != "unknown" {
		t.Fatal(state, e)
	}
	if send("POST", encode(), token, "").Code != 409 {
		t.Fatal("reupload")
	}
}
