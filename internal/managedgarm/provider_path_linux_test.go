package managedgarm

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalProviderPathsRejectBypasses(t *testing.T) {
	base := []byte(managedStockConfiguration(strings.Repeat("a", 64), strings.Repeat("b", 32)))
	raw, err := RenderManagedProviderConfiguration(base, []string{"repo-two", "repo-one"})
	if err != nil || validateProviderTOML(raw, []string{"repo-one", "repo-two"}) != nil {
		t.Fatal(err)
	}
	for _, v := range [][]byte{
		bytes.ReplaceAll(raw, []byte("v0.1.1"), []byte("v0.1.0")),
		bytes.ReplaceAll(raw, []byte("environment_variables = []"), []byte("environment_variables = [\"LD_\"]")),
		bytes.ReplaceAll(raw, []byte("/usr/local/bin/capacity-provider"), []byte("/usr/bin/sh")),
		bytes.ReplaceAll(raw, []byte("disable_jit_config = false"), []byte("disable_jit_config = true")),
		append(append([]byte(nil), raw...), []byte("\n[[provider]]\nname=\"extra\"\n")...),
		append([]byte("# unreviewed variant\n"), raw...),
	} {
		if validateProviderTOML(v, []string{"repo-one", "repo-two"}) == nil {
			t.Fatal("provider override accepted")
		}
	}
	if _, err := RenderManagedProviderConfiguration(base, []string{"same", "same"}); err == nil {
		t.Fatal("duplicate")
	}
}
func TestProviderCredentialsCannotReferenceNativeOrOtherRoles(t *testing.T) {
	controller := "11111111-1111-4111-8111-111111111111"
	raw, err := EncodeManagedProviderCredentials("repo-one", controller)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeProviderConfiguration(raw, "repo-one", controller); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{
		strings.Replace(string(raw), "stock-provider-v1", "prepared", 1),
		strings.Replace(string(raw), "127.0.0.1:9443", "native.invalid:8443", 1),
		strings.Replace(string(raw), "provider-repo-one-stock-key.pem", "api-key.pem", 1),
		strings.Replace(string(raw), `"gateway":`, `"Gateway":`, 1),
		strings.TrimSuffix(string(raw), "}") + `,"incus_key":"bad"}`,
	} {
		if _, err := decodeProviderConfiguration([]byte(v), "repo-one", controller); err == nil {
			t.Fatal("credential escape accepted")
		}
	}
}
func TestProviderProcessAndFilesFailClosed(t *testing.T) {
	valid := []byte("Uid:\t65530\t65530\t65530\t65530\nGid:\t65530\t65530\t65530\t65530\nCapEff:\t0000000000000000\nNoNewPrivs:\t1\n")
	if !providerProcessIdentity(valid) || providerProcessIdentity(bytes.ReplaceAll(valid, []byte("65530"), []byte("0"))) {
		t.Fatal("identity")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, _, err = providerFile(root, "config", 8, uint32(os.Geteuid()), false); err != nil {
		t.Fatal(err)
	}
	if _, _, err = providerFile(root, "../config", 8, uint32(os.Geteuid()), false); err == nil {
		t.Fatal("escape")
	}
	if err = os.Symlink("config", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, _, err = providerFile(root, "alias", 8, uint32(os.Geteuid()), false); err == nil {
		t.Fatal("symlink")
	}
	if _, _, err = providerFile(root, "config", 1, uint32(os.Geteuid()), false); err == nil {
		t.Fatal("unbounded")
	}
	o := &ProviderPathObserver{revoked: true}
	if _, err = o.Check(context.Background()); err == nil {
		t.Fatal("revocation reset")
	}
}
func TestActualPoolMappingRequiresEveryEnrolledProvider(t *testing.T) {
	controller := "11111111-1111-4111-8111-111111111111"
	id := "22222222-2222-4222-8222-222222222222"
	repo := "33333333-3333-4333-8333-333333333333"
	facts := stockidentity.Facts{Pool: id, Repository: repo, Provider: "repo-one", Flavor: "small", Image: "image", OSType: "linux", OSArch: "amd64"}
	p := admission.BrokerPoolProfile{Pool: id, Repository: repo, Sequence: 1, PoolFingerprint: facts.Fingerprint(), Authority: admission.StockProviderAuthority{Controller: controller, PoolID: id, ProviderName: "repo-one"}}
	profiles := map[string]admission.BrokerPoolProfile{id: p}
	row := map[string]any{"id": id, "repo_id": repo, "provider_name": "repo-one", "flavor": "small", "image": "image", "os_type": "linux", "os_arch": "amd64"}
	raw, _ := json.Marshal([]any{row})
	if verifyProviderPools(raw, profiles) != nil {
		t.Fatal("mapping")
	}
	row["provider_name"] = "other"
	raw, _ = json.Marshal([]any{row})
	if verifyProviderPools(raw, profiles) == nil {
		t.Fatal("other provider")
	}
	raw, _ = json.Marshal([]any{})
	if verifyProviderPools(raw, profiles) == nil {
		t.Fatal("missing pool")
	}
	if _, err := providerNames(profiles, controller); err != nil {
		t.Fatal(err)
	}
	other := p
	other.Pool = repo
	other.Authority.PoolID = repo
	profiles[repo] = other
	if _, err := providerNames(profiles, controller); err == nil {
		t.Fatal("one credential provider claimed multiple pools")
	}
}
