package adminview

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassets"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassettransfer"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprepare"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"golang.org/x/crypto/ssh"
)

func assetWorkflowFixture(t *testing.T) (*nativeprovision.Service, *nativeassettransfer.Service, string) {
	t.Helper()
	dir, e := os.MkdirTemp(".", ".assets-workflow-")
	if e != nil {
		t.Fatal(e)
	}
	dir, _ = filepath.Abs(dir)
	t.Cleanup(func() { os.RemoveAll(dir) })
	for _, n := range []string{"prepare", "assets", "prepare-journal", "asset-journal"} {
		os.Mkdir(filepath.Join(dir, n), 0700)
	}
	digest := func(v []byte) string { h := sha256.Sum256(v); return hex.EncodeToString(h[:]) }
	pm := nativeprepare.Manifest{Version: nativeprepare.Version, ID: strings.Repeat("a", 64), Files: map[string]string{}}
	for _, n := range nativeprepare.FileNames() {
		v := []byte(n)
		pm.Files[n] = digest(v)
		os.WriteFile(filepath.Join(dir, "prepare", n), v, 0600)
	}
	raw, _ := json.Marshal(pm)
	os.WriteFile(filepath.Join(dir, "prepare", "manifest.json"), raw, 0600)
	pb, e := nativeprovision.LoadBundle(filepath.Join(dir, "prepare"))
	if e != nil {
		t.Fatal(e)
	}
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(key)
	target := nativeprovision.Target{Address: "192.0.2.1", Port: 22, AdminUser: "administrator", HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), AdminKeyFingerprint: ssh.FingerprintSHA256(signer.PublicKey())}
	ps, e := nativeprovision.NewService(pb, target, filepath.Join(dir, "prepare-journal"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(ps.Close)
	am := nativeassets.Manifest{Version: nativeassets.Version, ID: strings.Repeat("c", 64), Architecture: "amd64", Files: map[string]nativeassets.Entry{}}
	for _, n := range nativeassets.Names() {
		path := filepath.Join(dir, "assets", n)
		f, e := os.Create(path)
		if e != nil {
			t.Fatal(e)
		}
		if n == "runtime.raw" {
			f.Truncate(64 << 20)
		} else {
			f.Write([]byte(n))
		}
		f.Close()
		f, _ = os.Open(path)
		hash := sha256.New()
		size, e := io.Copy(hash, f)
		f.Close()
		if e != nil {
			t.Fatal(e)
		}
		am.Files[n] = nativeassets.Entry{SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size}
	}
	raw, _ = json.Marshal(am)
	os.WriteFile(filepath.Join(dir, "assets", "manifest.json"), raw, 0600)
	helper := filepath.Join(dir, "helper")
	os.WriteFile(helper, []byte("unexecuted helper fixture"), 0600)
	ab, e := nativeassettransfer.LoadBundle(filepath.Join(dir, "assets"), helper, digest(raw), digest([]byte("unexecuted helper fixture")))
	if e != nil {
		t.Fatal(e)
	}
	as, e := nativeassettransfer.NewService(ab, target, filepath.Join(dir, "asset-journal"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(as.Close)
	return ps, as, pm.ID
}
func TestNativeAssetsSeparateReviewBindingAndDurableAcceptance(t *testing.T) {
	h, _, read := fixture(t)
	ps, as, prepID := assetWorkflowFixture(t)
	gate := make(chan struct{})
	defer close(gate)
	resolve := func() (ssh.Signer, error) { <-gate; return nil, nativeprovision.ErrInvalid }
	prepToken := strings.Repeat("b", 64)
	token := strings.Repeat("d", 64)
	if e := h.SetNativePreparation(map[string]NativePreparationTarget{"one": {Service: ps, Resolve: resolve}}, prepToken); e != nil {
		t.Fatal(e)
	}
	targets := map[string]NativeAssetsTarget{"one": {Service: as, PreparationID: prepID}}
	if h.SetNativeAssets(targets, read) == nil || h.SetNativeAssets(targets, prepToken) == nil {
		t.Fatal("role token reused")
	}
	if e := h.SetNativeAssets(targets, token); e != nil {
		t.Fatal(e)
	}
	if h.EnableManagement(token) == nil {
		t.Fatal("reverse role reuse")
	}
	send := func(method, body, credential, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://127.0.0.1/admin/api/v1/native-assets", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+credential)
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if send("GET", "", prepToken, "").Code != 401 || send("GET", "", token, "https://other.invalid").Code != 403 {
		t.Fatal("auth/origin")
	}
	w := send("GET", "", token, "")
	var view struct {
		Targets     []nativeAssetsView `json:"targets"`
		Unavailable []any              `json:"unavailable_targets"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || len(view.Targets) != 1 || len(view.Unavailable) != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	v := view.Targets[0]
	var total int64
	for _, f := range v.Files {
		total += f.Size
	}
	if len(v.Files) != 7 || total != v.BundleBytes || v.PreparationID != prepID || v.ActivationAuthorized {
		t.Fatal(v)
	}
	request := map[string]any{"target_id": "one", "preparation_id": prepID, "review_digest": as.ReviewDigest(), "inspect_only": false}
	encode := func() string { b, _ := json.Marshal(request); return string(b) }
	if send("POST", encode(), token, "").Code != 400 {
		t.Fatal("missing approval")
	}
	request["confirm_asset_staging"] = true
	request["preparation_id"] = strings.Repeat("f", 64)
	if send("POST", encode(), token, "").Code != 409 {
		t.Fatal("stale displayed preparation association accepted")
	}
	if state, e := as.Status(); e != nil || state.Status != "not_started" {
		t.Fatal("stale preparation ID had effects", state, e)
	}
	request["preparation_id"] = prepID
	request["path"] = "/tmp/forbidden"
	if send("POST", encode(), token, "").Code != 400 {
		t.Fatal("browser path accepted")
	}
	delete(request, "path")
	// A real preparation identity drift hides the approved asset recipe and blocks effects.
	h.nativeAssets["one"] = NativeAssetsTarget{Service: as, PreparationID: strings.Repeat("e", 64)}
	if send("POST", encode(), token, "").Code != 409 {
		t.Fatal("identity drift")
	}
	state, e := as.Status()
	if e != nil || state.Status != "not_started" {
		t.Fatal("drift mutated journal", state, e)
	}
	h.nativeAssets["one"] = targets["one"]
	w = send("POST", encode(), token, "")
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"preparation_id":"`+prepID+`"`) || !strings.Contains(w.Body.String(), `"attempt"`) || !strings.Contains(w.Body.String(), `"status":"unknown"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if send("POST", encode(), token, "").Code != 409 {
		t.Fatal("concurrent upload accepted")
	}
}
