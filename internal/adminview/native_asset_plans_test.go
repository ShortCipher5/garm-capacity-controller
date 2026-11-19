package adminview

import (
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
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativebundle"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"golang.org/x/crypto/ssh"
)

func planAssetFixture(t *testing.T, root string) (*nativeassettransfer.Bundle, nativeassets.Manifest) {
	t.Helper()
	dir := filepath.Join(root, "runtime-assets")
	if os.Mkdir(dir, 0700) != nil {
		t.Fatal("mkdir")
	}
	m := nativeassets.Manifest{Version: nativeassets.Version, ID: strings.Repeat("c", 64), Architecture: "amd64", Files: map[string]nativeassets.Entry{}}
	for _, n := range nativeassets.Names() {
		p := filepath.Join(dir, n)
		f, e := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			t.Fatal(e)
		}
		if n == "runtime.raw" {
			e = f.Truncate(64 << 20)
		} else {
			_, e = f.Write([]byte(n))
		}
		if e != nil {
			t.Fatal(e)
		}
		f.Seek(0, 0)
		h := sha256.New()
		size, e := io.Copy(h, f)
		f.Close()
		if e != nil {
			t.Fatal(e)
		}
		m.Files[n] = nativeassets.Entry{Size: size, SHA256: hex.EncodeToString(h.Sum(nil))}
	}
	raw, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600)
	helper := filepath.Join(root, "helper")
	os.WriteFile(helper, []byte("fixture"), 0600)
	md, _ := nativeassets.ManifestDigest(m)
	hash := sha256.Sum256([]byte("fixture"))
	b, e := nativeassettransfer.LoadBundle(dir, helper, md, hex.EncodeToString(hash[:]))
	if e != nil {
		t.Fatal(e)
	}
	return b, m
}
func TestDynamicAssetPlanReviewRegistrationAndNoSSH(t *testing.T) {
	_, planning, recipes, saved := assemblyTestFixture(t)
	recipe := recipes["host"]
	baseline := recipe.Build
	baseline.SelectionFile, _, _ = planning.LookupSaved(t.Context(), "host", saved.SelectionDigest)
	baseline.OutputDirectory = filepath.Join(recipe.OutputParent, "original")
	if _, e := nativebundle.Build(t.Context(), baseline); e != nil {
		t.Fatal(e)
	}
	pb, e := nativeprovision.LoadBundle(baseline.OutputDirectory)
	if e != nil {
		t.Fatal(e)
	}
	journal := filepath.Join(recipe.OutputParent, "journal")
	os.Mkdir(journal, 0700)
	original, e := nativeprovision.NewService(pb, recipe.Binding.NativeTarget, journal)
	if e != nil {
		t.Fatal(e)
	}
	defer original.Close()
	ab, m := planAssetFixture(t, recipe.OutputParent)
	md, _ := nativeassets.ManifestDigest(m)
	recipe.Build.RuntimeAssets = &nativebundle.AssetReference{Manifest: m, ManifestDigest: md}
	recipes["host"] = recipe
	h := &Handler{nativePlanning: planning, inFlight: make(chan struct{}, 16)}
	resolve := func() (ssh.Signer, error) {
		t.Error("local registration requested SSH credential")
		return nil, nativeprovision.ErrInvalid
	}
	if e = h.SetNativePreparation(map[string]NativePreparationTarget{"host": {original, resolve}}, strings.Repeat("e", 64)); e != nil {
		t.Fatal(e)
	}
	defer h.CloseNativePreparation()
	if e = h.ConfigureNativeAssembly(recipes); e != nil {
		t.Fatal(e)
	}
	c, e := h.nativeAssembly.Build(t.Context(), "host", saved.SelectionDigest)
	if e != nil {
		t.Fatal(e)
	}
	candidate, e := h.assemblyCandidate(h.nativePreparation["host"], c)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = candidate.RegisterReviewedReplacement(t.Context(), original, saved.SelectionDigest); e != nil {
		t.Fatal(e)
	}
	h.nativePreparation["host"] = NativePreparationTarget{candidate, resolve}
	token := strings.Repeat("f", 64)
	if e = h.SetNativeAssets(map[string]NativeAssetsTarget{}, token); e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(recipe.OutputParent, "registry")
	os.Mkdir(dir, 0700)
	reg, e := nativeassettransfer.NewRegistry(dir, map[string]*nativeassettransfer.Bundle{"slot": ab})
	if e != nil {
		t.Fatal(e)
	}
	defer reg.Close()
	if e = h.SetNativeAssetRegistry(reg); e != nil {
		t.Fatal(e)
	}
	call := func(method, path, body, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://controller.test/admin/api/v1/"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+auth)
		r.Header.Set("Origin", "https://controller.test")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "native-asset-plans", "", strings.Repeat("e", 64)); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := call("GET", "native-asset-plans", "", token)
	var list struct {
		Plans []nativeassettransfer.RegistrationReview
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Plans) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	v := list.Plans[0]
	body, _ := json.Marshal(map[string]any{"slot": v.Slot, "target_id": v.TargetID, "preparation_id": v.PreparationID, "review_digest": v.ReviewDigest, "confirm_asset_plan_registration": true})
	if w = call("POST", "native-asset-plans", string(body), token); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call("POST", "native-asset-plans", string(body), token); w.Code != 409 {
		t.Fatal("replayed", w.Code)
	}
	if w = call("GET", "native-assets", "", token); w.Code != 200 || !strings.Contains(w.Body.String(), v.AssetID) {
		t.Fatal(w.Code, w.Body.String())
	}
	state, e := h.nativeAssets["host"].Service.Status()
	if e != nil || state.Attempt != nil {
		t.Fatal("registration started SSH")
	}
	// A changed current owner configuration invalidates the registered asset view.
	h.nativePreparation["host"] = NativePreparationTarget{original, resolve}
	if _, _, ok := h.boundNativeAssets("host"); ok {
		t.Fatal("accepted stale assembly")
	}
	h.nativePreparation["host"] = NativePreparationTarget{candidate, resolve}
}
