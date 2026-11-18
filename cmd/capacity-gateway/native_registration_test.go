package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativebundle"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeegress"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprepare"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativezfs"
	"golang.org/x/crypto/ssh"
)

func registrationStartupFixture(t *testing.T) *adminConfiguration {
	t.Helper()
	cwd, _ := os.Getwd()
	root, e := os.MkdirTemp(cwd, ".registration-config-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	dir := func(n string) string {
		p := filepath.Join(root, n)
		if os.Mkdir(p, 0700) != nil {
			t.Fatal("mkdir")
		}
		return p
	}
	file := func(n string, b []byte) string {
		p := filepath.Join(root, n)
		if os.WriteFile(p, b, 0600) != nil {
			t.Fatal("write")
		}
		return p
	}
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(key)
	pk, _ := ssh.MarshalPrivateKey(key, "")
	admin := file("admin.key", pem.EncodeToMemory(pk))
	template, identity, export := dir("template"), dir("identity"), dir("export")
	h := strings.Repeat("a", 64)
	sums := ""
	for _, name := range nativeprepare.ExecutableNames() {
		sums += h + "  " + name + "\n"
	}
	if os.WriteFile(filepath.Join(export, "SHA256SUMS"), []byte(sums), 0444) != nil {
		t.Fatal("sums")
	}
	pin := sha256.Sum256([]byte(sums))
	var ca []byte
	for n, role := range []string{"owner-server", "owner-client"} {
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		cert := &x509.Certificate{SerialNumber: big.NewInt(int64(n + 1)), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
		der, e := x509.CreateCertificate(rand.Reader, cert, cert, pub, priv)
		if e != nil {
			t.Fatal(e)
		}
		encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		raw, _ := x509.MarshalPKCS8PrivateKey(priv)
		for suffix, b := range map[string][]byte{".crt": encoded, ".key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw})} {
			if os.WriteFile(filepath.Join(identity, role+suffix), b, 0600) != nil {
				t.Fatal("identity")
			}
		}
		ca = encoded
	}
	os.WriteFile(filepath.Join(identity, "controller-ca.crt"), ca, 0600)
	categories := map[string]int64{}
	for _, n := range []string{"buckets", "containers", "custom", "images", "virtual-machines", "deleted/buckets", "deleted/containers", "deleted/custom", "deleted/images", "deleted/virtual-machines"} {
		categories[n] = 1 << 20
	}
	cfg := map[string]any{"control": map[string]any{"policy": map[string]any{"version": "garm-native-policy/v5", "host": ""}, "controller_client_leaf": h, "controller_server_leaf": strings.Repeat("b", 64), "pool_reader": map[string]any{}}, "launch": map[string]any{}, "network_tools": map[string]any{}, "quota_reader": map[string]any{}, "artifacts": nativezfs.ArtifactConfiguration{Binary: "/usr/sbin/zfs", BinarySHA256: h, Helper: "/usr/libexec/zfs", HelperSHA256: h, ParentDataset: "tank/artifacts", ParentGUID: "2", QuotaBytes: 1 << 30, Image: "/private/runtime", ImageSHA256: h}, "storage": nativezfs.Configuration{Binary: "/usr/sbin/zfs", BinarySHA256: h, Helper: "/usr/libexec/zfs", HelperSHA256: h, RootDataset: "tank/native", RootGUID: "1", QuotaBytes: 1 << 30}, "storage_budgets": nativezfs.CategoryBudgets{Runtime: 1 << 20, Categories: categories}, "network": nativeegress.Specification{Mode: nativeegress.PublicInternet, TransitCIDR: "10.240.0.0/30", RunnerCIDR: "10.241.0.0/24", DNS: []string{"1.1.1.1"}, ControlPlaneCIDRs: []string{"8.8.8.8/32"}}}
	raw, _ := json.Marshal(cfg)
	os.WriteFile(filepath.Join(template, "capsule.json"), raw, 0600)
	catalog := nativebundle.RegistrationCatalog{Templates: map[string]nativebundle.RegistrationTemplate{"standard": {Configuration: nativebundle.Configuration{ExportDirectory: export, ExportManifestSHA256: hex.EncodeToString(pin[:]), PrivateDirectory: template, Architecture: "amd64"}, ArtifactReference: "image", StorageReference: "disk", NetworkReference: "network"}}, Identities: map[string]nativebundle.RegistrationIdentity{"owner": {Directory: identity, Template: "standard", Administrator: "admin"}}, Administrators: map[string]nativebundle.RegistrationAdministrator{"admin": {Networks: []string{"192.0.2.0/24"}, Users: []string{"root"}, Ports: []uint16{22}, AdminKeyFingerprint: ssh.FingerprintSHA256(signer.PublicKey())}}, PrivateParent: dir("private"), OutputParent: dir("output"), JournalParent: dir("journals")}
	return &adminConfiguration{EnableNativeRegistration: true, EnableSSHInventory: true, SSHInventory: &sshInventoryConfiguration{}, TokenFile: file("read-token", []byte(strings.Repeat("1", 64))), NativeRegistration: &nativeRegistrationConfiguration{Catalog: catalog, Directory: dir("registrations"), SelectionDirectory: dir("selections"), PlanningTokenFile: file("planning-token", []byte(strings.Repeat("2", 64))), PreparationTokenFile: file("preparation-token", []byte(strings.Repeat("3", 64))), AdminCredentialFiles: map[string]string{"admin": admin}}}
}
func TestNativeRegistrationLoadRealPrivateCatalogWithoutBaselinePlan(t *testing.T) {
	a := registrationStartupFixture(t)
	planning, preparation, resolvers, e := loadNativeRegistration(a)
	if e != nil || planning == preparation || len(resolvers) != 1 {
		t.Fatal(e)
	}
	key, e := resolvers["admin"]()
	if e != nil || ssh.FingerprintSHA256(key.PublicKey()) != a.NativeRegistration.Catalog.Administrators["admin"].AdminKeyFingerprint {
		t.Fatal(e)
	}
	// Later source replacement cannot change the frozen signer used after review.
	os.WriteFile(a.NativeRegistration.AdminCredentialFiles["admin"], []byte("changed"), 0600)
	again, e := resolvers["admin"]()
	if e != nil || ssh.FingerprintSHA256(again.PublicKey()) != ssh.FingerprintSHA256(key.PublicKey()) {
		t.Fatal("signer not frozen")
	}
}
func TestNativeRegistrationRejectsOptInRoleAndStaticMixes(t *testing.T) {
	for _, mode := range []string{"disabled", "inventory", "static-planning", "static-preparation", "static-assembly", "token-role", "admin-pin", "inventory-key"} {
		t.Run(mode, func(t *testing.T) {
			a := registrationStartupFixture(t)
			switch mode {
			case "disabled":
				a.EnableNativeRegistration = false
			case "inventory":
				a.EnableSSHInventory = false
			case "static-planning":
				a.NativePlanning = &nativePlanningConfiguration{}
			case "static-preparation":
				a.NativePreparation = &nativePreparationConfiguration{}
			case "static-assembly":
				a.EnableNativeAssembly = true
			case "token-role":
				a.NativeRegistration.PlanningTokenFile = a.NativeRegistration.PreparationTokenFile
			case "admin-pin":
				v := a.NativeRegistration.Catalog.Administrators["admin"]
				v.AdminKeyFingerprint = "SHA256:" + strings.Repeat("A", 43)
				a.NativeRegistration.Catalog.Administrators["admin"] = v
			case "inventory-key":
				a.SSHInventory.Keys = map[string]string{"restricted": a.NativeRegistration.AdminCredentialFiles["admin"]}
			}
			if _, _, _, e := loadNativeRegistration(a); e == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestNativeRegistrationActualAdminStartupExposesEmptyScopedWorkflow(t *testing.T) {
	a := registrationStartupFixture(t)
	keys := credentials(t)
	a.TLS = keys.serverFiles
	a.Listen = "127.0.0.1:0"
	root := filepath.Dir(a.TokenFile)
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(private, "")
	path := filepath.Join(root, "restricted.key")
	os.WriteFile(path, pem.EncodeToMemory(block), 0600)
	tokenPath := filepath.Join(root, "inventory-token")
	os.WriteFile(tokenPath, []byte(strings.Repeat("4", 64)), 0600)
	a.SSHInventory = &sshInventoryConfiguration{TokenFile: tokenPath, Keys: map[string]string{"inventory": path}}
	resources := admission.Resources{CPU: 1, RAM: 1, Runners: 1}
	store, e := admission.OpenSQLite(filepath.Join(root, "ledger.db"), admission.Config{Global: resources, Hosts: map[string]admission.Host{"host": {Budget: resources, Mode: "disabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool": {Budget: resources, Hosts: []string{"host"}}}, MaxObservationAge: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	server, closeAll, e := newAdminServer(a, store, false)
	if e != nil {
		t.Fatal(e)
	}
	defer closeAll()
	defer server.Close()
	read := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "https://127.0.0.1"+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		server.Handler.ServeHTTP(w, req)
		return w
	}
	response := read("/admin/api/v1/native-registration", strings.Repeat("3", 64))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var body struct {
		Catalog       nativebundle.RegistrationCatalogView `json:"catalog"`
		Registrations []any                                `json:"registrations"`
		Inventory     []any                                `json:"inventory_targets"`
	}
	if json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Catalog.Revision == "" || len(body.Registrations) != 0 || len(body.Inventory) != 0 {
		t.Fatal(response.Body.String())
	}
	if strings.Contains(response.Body.String(), root) || strings.Contains(response.Body.String(), "PRIVATE KEY") {
		t.Fatal("private startup data exposed")
	}
	if read("/admin/api/v1/native-registration", strings.Repeat("1", 64)).Code != 401 {
		t.Fatal("read token gained registration privilege")
	}
	preparation := read("/admin/api/v1/native-preparation", strings.Repeat("3", 64))
	if preparation.Code != 200 || !strings.Contains(preparation.Body.String(), `"targets":[]`) {
		t.Fatal("fake initial preparation", preparation.Code, preparation.Body.String())
	}
	closeAll()
	server.Close()
	// Restart sees only configuration journals. It still has no fabricated Plan.
	server, closeAll, e = newAdminServer(a, store, false)
	if e != nil {
		t.Fatal(e)
	}
	defer closeAll()
	defer server.Close()
	if read("/admin/api/v1/native-registration", strings.Repeat("3", 64)).Code != 200 {
		t.Fatal("restart")
	}
}

func TestNativeRegistrationMixedModeRefusesBeforePrivateFileReads(t *testing.T) {
	a := &adminConfiguration{EnableNativeRegistration: true, EnableSSHInventory: true, SSHInventory: &sshInventoryConfiguration{}, NativeRegistration: &nativeRegistrationConfiguration{}, NativePlanning: &nativePlanningConfiguration{}, TokenFile: "/not-present/private-token"}
	if _, closeAll, e := newAdminServer(a, nil, false); e != adminview.ErrConfiguration {
		closeAll()
		t.Fatal("did not refuse conflicting mode before private reads", e)
	}
}
