//go:build linux

package adminview

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativebundle"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeegress"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeplanning"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeregistration"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativezfs"
	"golang.org/x/crypto/ssh"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDynamicRegistrationDiscoveryAssemblyInitialPlanAndRestart(t *testing.T) {
	_, _, recipes, _ := assemblyTestFixture(t)
	base := recipes["host"].Build
	inventory, binding := assemblyTestInventory(t)
	raw, e := os.ReadFile(filepath.Join(base.PrivateDirectory, "capsule.json"))
	if e != nil {
		t.Fatal(e)
	}
	var cfg map[string]any
	json.Unmarshal(raw, &cfg)
	cfg["control"].(map[string]any)["policy"].(map[string]any)["host"] = ""
	hash := strings.Repeat("a", 64)
	cfg["artifacts"] = nativezfs.ArtifactConfiguration{Binary: "/usr/sbin/zfs", BinarySHA256: hash, Helper: "/usr/libexec/zfs-exec", HelperSHA256: hash, ParentDataset: "tank/artifacts", ParentGUID: "2", QuotaBytes: 1 << 30, Image: "/private/image", ImageSHA256: hash}
	cfg["storage"] = nativezfs.Configuration{Binary: "/usr/sbin/zfs", BinarySHA256: hash, Helper: "/usr/libexec/zfs-exec", HelperSHA256: hash, RootDataset: "tank/native", RootGUID: "1", QuotaBytes: 1 << 30}
	categories := map[string]int64{}
	for _, n := range []string{"buckets", "containers", "custom", "images", "virtual-machines", "deleted/buckets", "deleted/containers", "deleted/custom", "deleted/images", "deleted/virtual-machines"} {
		categories[n] = 1 << 20
	}
	cfg["storage_budgets"] = nativezfs.CategoryBudgets{Runtime: 1 << 20, Categories: categories}
	cfg["network"] = nativeegress.Specification{Mode: nativeegress.PublicInternet, TransitCIDR: "10.240.0.0/30", RunnerCIDR: "10.241.0.0/24", DNS: []string{"1.1.1.1"}, ControlPlaneCIDRs: []string{"8.8.8.8/32"}}
	raw, _ = json.Marshal(cfg)
	os.WriteFile(filepath.Join(base.PrivateDirectory, "capsule.json"), raw, 0600)
	cwd, e := os.Getwd()
	if e != nil {
		t.Fatal(e)
	}
	dir := func() string {
		// The production registry rejects writable ancestors, including /tmp.
		// Keep this fixture beneath the protected checkout on every runner.
		p, err := os.MkdirTemp(cwd, ".registration-test-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(p) })
		return p
	}
	base.ID = ""
	base.ExpectedBindingFile = ""
	catalog := nativebundle.RegistrationCatalog{Templates: map[string]nativebundle.RegistrationTemplate{"standard": {Configuration: base, ArtifactReference: "image", StorageReference: "storage", NetworkReference: "network"}}, Identities: map[string]nativebundle.RegistrationIdentity{"owner": {Directory: base.PrivateDirectory, Template: "standard", Administrator: "admin"}}, Administrators: map[string]nativebundle.RegistrationAdministrator{"admin": {Networks: []string{"192.0.2.0/24"}, Users: []string{binding.NativeTarget.AdminUser}, Ports: []uint16{binding.NativeTarget.Port}, AdminKeyFingerprint: binding.NativeTarget.AdminKeyFingerprint}}, PrivateParent: dir(), OutputParent: dir(), JournalParent: dir()}
	registry, saved := dir(), dir()
	planningToken, preparationToken := strings.Repeat("d", 64), strings.Repeat("e", 64)
	resolvers := map[string]func() (ssh.Signer, error){"admin": func() (ssh.Signer, error) {
		t.Error("configuration flow resolved admin credential")
		return nil, nativeprovision.ErrInvalid
	}}
	h := &Handler{sshInventory: inventory, inFlight: make(chan struct{}, 16)}
	if e = h.ConfigureNativeRegistration(catalog, registry, saved, planningToken, preparationToken, resolvers); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(h.CloseNativePreparation)
	call := func(handler *Handler, method, path string, v any, token string) *httptest.ResponseRecorder {
		var body []byte
		if v != nil {
			body, _ = json.Marshal(v)
		}
		r := httptest.NewRequest(method, "https://controller.test/admin/api/v1/"+path, strings.NewReader(string(body)))
		r.Header.Set("Origin", "https://controller.test")
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := call(h, http.MethodGet, "native-registration", nil, planningToken); w.Code != 401 {
		t.Fatal(w.Code)
	}
	rBad := httptest.NewRequest(http.MethodGet, "https://controller.test/admin/api/v1/native-registration", nil)
	rBad.Header.Set("Authorization", "Bearer "+preparationToken)
	rBad.Header.Set("Origin", "https://evil.test")
	wBad := httptest.NewRecorder()
	h.ServeHTTP(wBad, rBad)
	if wBad.Code != 403 {
		t.Fatal("cross-origin registration", wBad.Code)
	}
	request := nativeregistration.Request{Revision: h.nativeRegistration.CatalogView().Revision, Template: "standard", Identity: "owner", Administrator: "admin", ArtifactReference: "image", StorageReference: "storage", NetworkReference: "network", PreparationID: binding.PreparationID, Host: binding.Host, NativeTarget: binding.NativeTarget, InventoryTargetID: binding.InventoryTarget.ID, InventoryTargetDigest: nativeplanning.Digest(binding.InventoryTarget)}
	w := call(h, http.MethodPost, "native-registration", map[string]any{"action": "review", "configuration": request}, preparationToken)
	var review struct {
		Review nativeregistration.Report `json:"review"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &review) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call(h, http.MethodPost, "native-registration", map[string]any{"action": "register", "configuration": request, "review_digest": review.Review.RegistrationDigest, "confirm_register": true}, preparationToken)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(h.nativePreparation) != 0 {
		t.Fatal("fake baseline")
	}
	w = call(h, http.MethodGet, "native-assembly", nil, preparationToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "pending_targets") {
		t.Fatal(w.Code, w.Body.String())
	}
	proposal, e := h.nativePlanning.Probe(context.Background(), binding.InventoryTarget.ID, nativeplanning.Digest(binding), "11111111-1111-4111-8111-111111111111")
	if e != nil {
		t.Fatal(e)
	}
	selection, e := h.nativePlanning.Save(t.Context(), binding.InventoryTarget.ID, proposal.ReviewDigest, proposal.Defaults)
	if e != nil {
		t.Fatal(e)
	}
	built, e := h.nativeAssembly.Build(t.Context(), binding.InventoryTarget.ID, selection.SelectionDigest)
	if e != nil {
		t.Fatal(e)
	}
	action := map[string]any{"action": "initial_plan", "target_id": binding.InventoryTarget.ID, "selection_digest": selection.SelectionDigest, "assembly_digest": built.Report().AssemblyDigest, "registration_digest": review.Review.RegistrationDigest, "confirm_initial_plan": true}
	w = call(h, http.MethodPost, "native-registration", action, preparationToken)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	current := h.nativePreparation[binding.InventoryTarget.ID].Service
	w = call(h, http.MethodGet, "native-registration", nil, preparationToken)
	if w.Code != 200 || h.nativePreparation[binding.InventoryTarget.ID].Service != current {
		t.Fatal("history replaced active service", w.Code, w.Body.String())
	}
	restarted := &Handler{sshInventory: inventory, inFlight: make(chan struct{}, 16)}
	if e = restarted.ConfigureNativeRegistration(catalog, registry, saved, planningToken, preparationToken, resolvers); e != nil {
		t.Fatal("restart", e)
	}
	t.Cleanup(restarted.CloseNativePreparation)
	v, e := restarted.nativePreparation[binding.InventoryTarget.ID].Service.View(binding.InventoryTarget.ID)
	if e != nil || v.ReviewDigest != built.Report().PlanDigest {
		t.Fatal(v, e)
	}
	w = call(h, http.MethodPost, "native-registration", map[string]any{"action": "register", "configuration": request, "review_digest": review.Review.RegistrationDigest, "confirm_register": true}, preparationToken)
	if w.Code != 409 {
		t.Fatal("replayed registration", w.Code)
	}
}
