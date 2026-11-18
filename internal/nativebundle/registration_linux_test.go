//go:build linux

package nativebundle

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeegress"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativezfs"
)

func registrationFixture(t *testing.T) (RegistrationCatalog, RegistrationRequest) {
	t.Helper()
	base, cfg := fixture(t)
	selection := selectionFixture(t, &base, cfg)
	cfg["control"].(map[string]any)["policy"].(map[string]any)["host"] = ""
	h := strings.Repeat("a", 64)
	cfg["artifacts"] = nativezfs.ArtifactConfiguration{Binary: "/usr/sbin/zfs", BinarySHA256: h, Helper: "/usr/libexec/zfs-exec", HelperSHA256: h, ParentDataset: "tank/artifacts", ParentGUID: "2", QuotaBytes: 1 << 30, Image: "/private/image", ImageSHA256: h}
	cfg["storage"] = nativezfs.Configuration{Binary: "/usr/sbin/zfs", BinarySHA256: h, Helper: "/usr/libexec/zfs-exec", HelperSHA256: h, RootDataset: "tank/native", RootGUID: "1", QuotaBytes: 1 << 30}
	cats := map[string]int64{}
	for _, n := range []string{"buckets", "containers", "custom", "images", "virtual-machines", "deleted/buckets", "deleted/containers", "deleted/custom", "deleted/images", "deleted/virtual-machines"} {
		cats[n] = 1 << 20
	}
	cfg["storage_budgets"] = nativezfs.CategoryBudgets{Runtime: 1 << 20, Categories: cats}
	cfg["network"] = nativeegress.Specification{Mode: nativeegress.PublicInternet, TransitCIDR: "10.240.0.0/30", RunnerCIDR: "10.241.0.0/24", DNS: []string{"1.1.1.1"}, ControlPlaneCIDRs: []string{"8.8.8.8/32"}}
	writeConfig(t, base, cfg)
	identityDir := t.TempDir()
	for _, n := range []string{"owner-server.crt", "owner-server.key", "owner-client.crt", "owner-client.key", "controller-ca.crt"} {
		b, e := os.ReadFile(filepath.Join(base.PrivateDirectory, n))
		if e != nil {
			t.Fatal(e)
		}
		if os.WriteFile(filepath.Join(identityDir, n), b, 0600) != nil {
			t.Fatal("copy")
		}
	}
	base.ID = ""
	base.OutputDirectory = ""
	base.SelectionFile = ""
	base.ExpectedBindingFile = ""
	c := RegistrationCatalog{Templates: map[string]RegistrationTemplate{"standard": {Configuration: base, ArtifactReference: "image1", StorageReference: "disk1", NetworkReference: "egress1"}}, Identities: map[string]RegistrationIdentity{"owner1": {Directory: identityDir, Template: "standard", Administrator: "ssh", HostPrefix: "host-"}}, Administrators: map[string]RegistrationAdministrator{"ssh": {Networks: []string{"192.0.2.0/24"}, Users: []string{"root"}, Ports: []uint16{22}, AdminKeyFingerprint: selection.Binding.NativeTarget.AdminKeyFingerprint}}, PrivateParent: t.TempDir(), OutputParent: t.TempDir(), JournalParent: t.TempDir()}
	r := RegistrationRequest{Template: "standard", Identity: "owner1", Administrator: "ssh", ArtifactReference: "image1", StorageReference: "disk1", NetworkReference: "egress1", PreparationID: selection.Binding.PreparationID, Host: "host-new", NativeTarget: selection.Binding.NativeTarget, InventoryTarget: selection.Binding.InventoryTarget}
	for _, p := range []string{base.PrivateDirectory, identityDir, c.PrivateParent, c.OutputParent, c.JournalParent} {
		if os.Chmod(p, 0700) != nil {
			t.Fatal("chmod")
		}
	}
	return c, r
}
func TestRegistrationCompilesNewScopedTargetAndFrozenPrivateInputs(t *testing.T) {
	c, r := registrationFixture(t)
	compiler, e := NewRegistrationCompiler(c)
	if e != nil {
		t.Fatal(e)
	}
	r.Revision = compiler.Revision()
	// An explicit new pinned endpoint is allowed inside the approved network.
	r.NativeTarget.Address = "192.0.2.77"
	compiled, e := compiler.Compile(r)
	if e != nil {
		t.Fatal(e)
	}
	m := compiled.Metadata()
	if m.Binding.NativeTarget.Address != r.NativeTarget.Address || m.Binding.Host != r.Host || m.RegistrationDigest == "" || m.ActivationAuthorized || m.OwnerServerSPKI == m.OwnerClientSPKI {
		t.Fatal(m)
	}
	// Freeze ownership: later private source edits cannot alter compiled output.
	if os.WriteFile(filepath.Join(c.Identities["owner1"].Directory, "owner-client.key"), []byte("changed"), 0600) != nil {
		t.Fatal("mutate")
	}
	if compiled.Materialize(context.Background()) != nil {
		t.Fatal("materialize")
	}
	if compiled.InspectMaterialized(context.Background()) != nil {
		t.Fatal("inspect materialized")
	}
	build := m.Build
	build.OutputDirectory = filepath.Join(c.OutputParent, "bundle")
	// Selection is intentionally not applied here: this verifies real private
	// materialization feeds the existing builder, not capacity or host qualification.
	build.ExpectedBindingFile = ""
	result, e := Build(context.Background(), build)
	if e != nil || result.ActivationAuthorized {
		t.Fatal(result, e)
	}
	raw, e := os.ReadFile(filepath.Join(build.PrivateDirectory, "capsule.json"))
	if e != nil {
		t.Fatal(e)
	}
	v, e := decodeObject(raw)
	if e != nil {
		t.Fatal(e)
	}
	control, _ := object(v, "control")
	policy, _ := object(control, "policy")
	if get(policy, "host") != r.Host {
		t.Fatal("host not compiled")
	}
	var binding map[string]any
	raw, _ = os.ReadFile(m.Build.ExpectedBindingFile)
	if json.Unmarshal(raw, &binding) != nil {
		t.Fatal("binding")
	}
	if compiled.Materialize(context.Background()) == nil {
		t.Fatal("materialization replay")
	}
	if os.WriteFile(filepath.Join(build.PrivateDirectory, "capsule.json"), []byte("changed"), 0600) != nil {
		t.Fatal("change")
	}
	if compiled.InspectMaterialized(context.Background()) == nil {
		t.Fatal("changed input inspected")
	}
}
func TestRegistrationRejectsScopeAndProfileDrift(t *testing.T) {
	c, r := registrationFixture(t)
	compiler, e := NewRegistrationCompiler(c)
	if e != nil {
		t.Fatal(e)
	}
	r.Revision = compiler.Revision()
	for name, change := range map[string]func(*RegistrationRequest){"revision": func(r *RegistrationRequest) { r.Revision = strings.Repeat("f", 64) }, "network": func(r *RegistrationRequest) { r.NativeTarget.Address = "198.51.100.2" }, "user": func(r *RegistrationRequest) { r.NativeTarget.AdminUser = "other" }, "port": func(r *RegistrationRequest) { r.NativeTarget.Port = 2222 }, "admin-key": func(r *RegistrationRequest) { r.NativeTarget.AdminKeyFingerprint = "SHA256:" + strings.Repeat("A", 43) }, "host": func(r *RegistrationRequest) { r.Host = "other" }, "artifact": func(r *RegistrationRequest) { r.ArtifactReference = "other" }, "storage": func(r *RegistrationRequest) { r.StorageReference = "other" }, "network-profile": func(r *RegistrationRequest) { r.NetworkReference = "other" }, "identity": func(r *RegistrationRequest) { r.Identity = "unknown" }, "traversal": func(r *RegistrationRequest) { r.PreparationID = "../unsafe" }} {
		t.Run(name, func(t *testing.T) {
			bad := r
			change(&bad)
			if _, e := compiler.Compile(bad); e == nil {
				t.Fatal("accepted")
			}
		})
	}
	if entries, e := os.ReadDir(c.PrivateParent); e != nil || len(entries) != 0 {
		t.Fatal("compile wrote", e)
	}
}
func TestRegistrationRefusesIdentityReuseAndProfileInvalidity(t *testing.T) {
	for _, mode := range []string{"shared-key", "quota", "unknown-section-field", "host-template", "duplicate-key"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := registrationFixture(t)
			template := c.Templates["standard"]
			switch mode {
			case "shared-key":
				c.Identities["second"] = c.Identities["owner1"]
			case "duplicate-key":
				os.WriteFile(filepath.Join(template.Configuration.PrivateDirectory, "capsule.json"), []byte(`{"control":{},"CONTROL":{}}`), 0600)
			default:
				raw, _ := os.ReadFile(filepath.Join(template.Configuration.PrivateDirectory, "capsule.json"))
				v, _ := decodeObject(raw)
				switch mode {
				case "quota":
					v["storage"].(map[string]any)["QuotaBytes"] = 0
				case "unknown-section-field":
					v["artifacts"].(map[string]any)["untrusted"] = true
				case "host-template":
					v["control"].(map[string]any)["policy"].(map[string]any)["host"] = "already-bound"
				}
				writeConfig(t, template.Configuration, v)
			}
			if _, e := NewRegistrationCompiler(c); e == nil {
				t.Fatal("accepted")
			}
		})
	}
}
func TestRegistrationCopiedMaterializationIsOneShotAndPartialNeverAdopted(t *testing.T) {
	c, r := registrationFixture(t)
	compiler, e := NewRegistrationCompiler(c)
	if e != nil {
		t.Fatal(e)
	}
	r.Revision = compiler.Revision()
	compiled, e := compiler.Compile(r)
	if e != nil {
		t.Fatal(e)
	}
	var successes atomic.Int64
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func(copy Registration) {
			defer wg.Done()
			if copy.Materialize(context.Background()) == nil {
				successes.Add(1)
			}
		}(compiled)
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal(successes.Load())
	}
	another, e := compiler.Compile(r)
	if e != nil {
		t.Fatal(e)
	}
	if another.Materialize(context.Background()) == nil {
		t.Fatal("existing private directory adopted")
	}
}

func TestRegistrationCatalogViewIsSanitizedAndOwned(t *testing.T) {
	c, r := registrationFixture(t)
	compiler, e := NewRegistrationCompiler(c)
	if e != nil {
		t.Fatal(e)
	}
	view := compiler.CatalogView()
	raw, e := json.Marshal(view)
	if e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{c.PrivateParent, c.OutputParent, c.JournalParent, c.Identities["owner1"].Directory, "PRIVATE KEY", "AdminKeyFingerprint", "192.0.2.0/24"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private catalog detail exposed")
		}
	}
	if view.Revision != compiler.Revision() || len(view.Templates) != 1 || view.Templates[0].ArtifactReference != "image1" || len(view.Identities) != 1 || view.Identities[0].Name != "owner1" || view.Administrators[0] != "ssh" {
		t.Fatal(view)
	}
	view.Templates[0].ArtifactReference = "changed"
	view.Identities[0].Template = "changed"
	view.Administrators[0] = "changed"
	r.Revision = compiler.Revision()
	if _, e := compiler.Compile(r); e != nil {
		t.Fatal("view mutation altered compiler", e)
	}
	if compiler.CatalogView().Templates[0].ArtifactReference != "image1" {
		t.Fatal("view storage shared")
	}
}
