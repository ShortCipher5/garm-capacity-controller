//go:build linux

package nativeregistration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capsulepeer"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativebundle"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeegress"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeplanning"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprepare"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativezfs"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func cert(t *testing.T, serial int64) ([]byte, []byte) {
	t.Helper()
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	v := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "fixture"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, IsCA: true, BasicConstraintsValid: true}
	der, e := x509.CreateCertificate(rand.Reader, v, v, pub, key)
	if e != nil {
		t.Fatal(e)
	}
	pk, _ := x509.MarshalPKCS8PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk})
}
func elfFixture() []byte {
	b := make([]byte, 64)
	copy(b, []byte{127, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(b[16:], 2)
	binary.LittleEndian.PutUint16(b[18:], 62)
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint16(b[52:], 64)
	return b
}
func fixture(t *testing.T) (nativebundle.Configuration, map[string]any) {
	t.Helper()
	e, p, parent := protectedTestDirectory(t), protectedTestDirectory(t), protectedTestDirectory(t)
	c := nativebundle.Configuration{ExportDirectory: e, PrivateDirectory: p, OutputDirectory: filepath.Join(parent, "bundle"), ID: strings.Repeat("c", 64), Architecture: "amd64"}
	sums := ""
	for _, n := range nativeprepare.ExecutableNames() {
		b := elfFixture()
		if os.WriteFile(filepath.Join(e, n), b, 0555) != nil {
			t.Fatal("write")
		}
		sums += digest(b) + "  " + n + "\n"
	}
	os.WriteFile(filepath.Join(e, "SHA256SUMS"), []byte(sums), 0444)
	c.ExportManifestSHA256 = digest([]byte(sums))
	sc, sk := cert(t, 1)
	cc, ck := cert(t, 2)
	for n, b := range map[string][]byte{"owner-server.crt": sc, "owner-server.key": sk, "owner-client.crt": cc, "owner-client.key": ck, "controller-ca.crt": sc} {
		os.WriteFile(filepath.Join(p, n), b, 0600)
	}
	cfg := map[string]any{"control": map[string]any{"policy": map[string]any{"version": "garm-native-policy/v5", "domain_id": ""}, "controller_client_leaf": strings.Repeat("a", 64), "controller_server_leaf": strings.Repeat("b", 64), "pool_reader": map[string]any{}}, "launch": map[string]any{}, "storage": map[string]any{}, "artifacts": map[string]any{}, "network_tools": map[string]any{}, "quota_reader": map[string]any{}}
	writeConfig(t, c, cfg)
	return c, cfg
}
func writeConfig(t *testing.T, c nativebundle.Configuration, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if os.WriteFile(filepath.Join(c.PrivateDirectory, "capsule.json"), b, 0600) != nil {
		t.Fatal("config")
	}
}

func selectionFixture(t *testing.T, c *nativebundle.Configuration, cfg map[string]any) nativeplanning.Selection {
	t.Helper()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewPublicKey(pub)
	binding := nativeplanning.Binding{PreparationID: c.ID, Host: "host-a", NativeTarget: nativeprovision.Target{Address: "192.0.2.10", Port: 22, AdminUser: "root", HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), AdminKeyFingerprint: ssh.FingerprintSHA256(key)}, InventoryTarget: sshinventory.Target{ID: "inventory-a", Address: "127.0.0.1", Port: 22, User: "inventory", CredentialRef: "key", HostKeyFingerprint: ssh.FingerprintSHA256(key), ReviewDigest: strings.Repeat("a", 64)}}
	cpus, ram := int64(8), int64(8192)
	now := time.Now().UTC().Add(-time.Second)
	unit := nativeboundary.Limits{MemoryBytes: 512 << 20, CPUMilli: 500, Tasks: 32}
	s := nativeplanning.Selection{Version: nativeplanning.Version, Binding: binding, Observation: sshinventory.Result{Source: "authenticated_linux_os_view", ObservedAt: now, LogicalCPUs: &cpus, RAMMiB: &ram}, SavedAt: now, ExpiresAt: now.Add(nativeplanning.Lifetime), ReserveCPUMilli: 1000, ReserveRAMMiB: 1024, Limits: nativeboundary.Limits{MemoryBytes: 4 << 30, CPUMilli: 4000, Tasks: 256}, Partition: capsulepeer.PartitionSpec{Bootstrap: unit, Daemon: unit, Monitors: unit, Guests: unit}}
	s.ObservationDigest = nativeplanning.ObservationDigest(binding, s.Observation)
	cfg["control"].(map[string]any)["policy"].(map[string]any)["host"] = "host-a"
	cfg["limits"] = map[string]any{"unselected": true}
	cfg["partition"] = map[string]any{"unselected": true}
	cfg["storage"].(map[string]any)["quota_bytes"] = json.Number("9007199254740993")
	writeConfig(t, *c, cfg)
	c.SelectionFile = filepath.Join(c.PrivateDirectory, "selection.json")
	c.ExpectedBindingFile = filepath.Join(c.PrivateDirectory, "binding.json")
	saveSelection(t, c.ExpectedBindingFile, binding)
	saveSelection(t, c.SelectionFile, s)
	return s
}
func saveSelection(t *testing.T, path string, v any) {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil || os.WriteFile(path, b, 0600) != nil {
		t.Fatal("write selection")
	}
}

func registrationFixture(t *testing.T) (nativebundle.RegistrationCatalog, nativebundle.RegistrationRequest) {
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
	identityDir := protectedTestDirectory(t)
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
	c := nativebundle.RegistrationCatalog{Templates: map[string]nativebundle.RegistrationTemplate{"standard": {Configuration: base, ArtifactReference: "image1", StorageReference: "disk1", NetworkReference: "egress1"}}, Identities: map[string]nativebundle.RegistrationIdentity{"owner1": {Directory: identityDir, Template: "standard", Administrator: "ssh", HostPrefix: "host-"}}, Administrators: map[string]nativebundle.RegistrationAdministrator{"ssh": {Networks: []string{"192.0.2.0/24"}, Users: []string{"root"}, Ports: []uint16{22}, AdminKeyFingerprint: selection.Binding.NativeTarget.AdminKeyFingerprint}}, PrivateParent: protectedTestDirectory(t), OutputParent: protectedTestDirectory(t), JournalParent: protectedTestDirectory(t)}
	r := nativebundle.RegistrationRequest{Template: "standard", Identity: "owner1", Administrator: "ssh", ArtifactReference: "image1", StorageReference: "disk1", NetworkReference: "egress1", PreparationID: selection.Binding.PreparationID, Host: "host-new", NativeTarget: selection.Binding.NativeTarget, InventoryTarget: selection.Binding.InventoryTarget}
	for _, p := range []string{base.PrivateDirectory, identityDir, c.PrivateParent, c.OutputParent, c.JournalParent} {
		if os.Chmod(p, 0700) != nil {
			t.Fatal("chmod")
		}
	}
	return c, r
}

func registryFixture(t *testing.T) (*Service, Request, nativebundle.RegistrationCatalog) {
	t.Helper()
	catalog, req := registrationFixture(t)
	compiler, e := nativebundle.NewRegistrationCompiler(catalog)
	if e != nil {
		t.Fatal(e)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	key, _ := ssh.NewPublicKey(pub)
	inventory, e := sshinventory.NewService(map[string]sshinventory.Config{req.InventoryTarget.ID: {Address: "127.0.0.1", Port: 22, User: "inventory", HostKey: string(ssh.MarshalAuthorizedKey(key)), CredentialRef: "inventory-key"}}, func(context.Context, string) (ssh.Signer, error) {
		t.Fatal("registration resolved SSH credential")
		return signer, nil
	}, func(context.Context, string, string, string, string, bool) error {
		t.Fatal("registration probed SSH")
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	target := inventory.Targets()[0]
	r := Request{Revision: compiler.Revision(), Template: req.Template, Identity: req.Identity, Administrator: req.Administrator, ArtifactReference: req.ArtifactReference, StorageReference: req.StorageReference, NetworkReference: req.NetworkReference, PreparationID: req.PreparationID, Host: req.Host, InventoryTargetID: target.ID, InventoryTargetDigest: nativeplanning.Digest(target), NativeTarget: req.NativeTarget}
	dir := protectedTestDirectory(t)
	os.Chmod(dir, 0700)
	svc, e := NewService(compiler, inventory, dir)
	if e != nil {
		t.Fatal(e)
	}
	return svc, r, catalog
}
func TestRegisterActualMaterializationRestoreAndNoReplay(t *testing.T) {
	s, r, _ := registryFixture(t)
	review, e := s.Review(t.Context(), r)
	if e != nil {
		t.Fatal(e)
	}
	c, e := s.Register(t.Context(), r, review.RegistrationDigest)
	if e != nil {
		t.Fatal(e)
	}
	if c.Metadata().Binding.InventoryTarget.ID != r.InventoryTargetID {
		t.Fatal("wrong target")
	}
	if _, e = s.Register(t.Context(), r, review.RegistrationDigest); e == nil {
		t.Fatal("replayed")
	}
	restored, h, e := s.Restore(t.Context())
	if e != nil || len(restored) != 1 || len(h) != 1 || h[0].ActivationAuthorized || h[0].State != "configuration_registered" {
		t.Fatal(h, e)
	}
	raw, e := os.ReadFile(filepath.Join(c.Metadata().Build.PrivateDirectory, "binding.json"))
	if e != nil {
		t.Fatal(e)
	}
	var b nativeplanning.Binding
	if json.Unmarshal(raw, &b) != nil || b != c.Metadata().Binding {
		t.Fatal("wrong real binding")
	}
	os.WriteFile(filepath.Join(c.Metadata().Build.PrivateDirectory, "capsule.json"), []byte("changed"), 0600)
	if _, _, e = s.Restore(t.Context()); e == nil {
		t.Fatal("adopted drift")
	}
}
func TestRegistrationUnknownConsumesIdentityAndRejectsAliasReuse(t *testing.T) {
	s, r, catalog := registryFixture(t)
	review, e := s.Review(t.Context(), r)
	if e != nil {
		t.Fatal(e)
	}
	// An existing private path causes fresh materialization to fail after intent.
	p := filepath.Join(catalog.PrivateParent, r.PreparationID)
	os.Mkdir(p, 0700)
	if _, e = s.Register(t.Context(), r, review.RegistrationDigest); e == nil {
		t.Fatal("adopted")
	}
	os.Remove(p) // Repair is not permission to replay.
	if _, e = s.Register(t.Context(), r, review.RegistrationDigest); e == nil {
		t.Fatal("retried unknown")
	}
	r.PreparationID = strings.Repeat("e", 64)
	r.Host = "host-other"
	rev, e := s.Review(t.Context(), r)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Register(t.Context(), r, rev.RegistrationDigest); e == nil {
		t.Fatal("reused identity")
	}
	out, h, e := s.Restore(t.Context())
	if e != nil || len(out) != 0 || len(h) != 1 || h[0].State != "registration_unknown" {
		t.Fatal(h, e)
	}
}
func TestRegistrationConcurrentInstancesConsumeOnce(t *testing.T) {
	s, r, _ := registryFixture(t)
	other, e := NewService(s.compiler, s.inventory, s.directory)
	if e != nil {
		t.Fatal(e)
	}
	review, e := s.Review(t.Context(), r)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, svc := range []*Service{s, other} {
		wg.Add(1)
		go func(x *Service) {
			defer wg.Done()
			_, e := x.Register(context.Background(), r, review.RegistrationDigest)
			results <- e
		}(svc)
	}
	wg.Wait()
	close(results)
	n := 0
	for e := range results {
		if e == nil {
			n++
		}
	}
	if n != 1 {
		t.Fatal(n)
	}
}
func TestRegistrationRejectsOrphansFIFOAndWrongReviewBeforeEffects(t *testing.T) {
	for _, mode := range []string{"orphan", "fifo", "review", "inventory", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, r, c := registryFixture(t)
			rev, e := s.Review(t.Context(), r)
			if e != nil {
				t.Fatal(e)
			}
			ctx := t.Context()
			switch mode {
			case "orphan":
				s.write(r.PreparationID+".complete.json", completed{version, rev.RegistrationDigest})
			case "fifo":
				if unix.Mkfifo(filepath.Join(s.directory, r.PreparationID+".intent.json"), 0600) != nil {
					t.Fatal("fifo")
				}
			case "review":
				rev.RegistrationDigest = strings.Repeat("e", 64)
			case "inventory":
				r.InventoryTargetDigest = strings.Repeat("f", 64)
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, e = s.Register(ctx, r, rev.RegistrationDigest); e == nil {
				t.Fatal("accepted")
			}
			if _, e = os.Stat(filepath.Join(c.PrivateParent, r.PreparationID)); !os.IsNotExist(e) {
				t.Fatal("materialized invalid request")
			}
		})
	}
}

func TestRegistrationBoundMatchesInstallableSlotsAndCatalogIsFrozen(t *testing.T) {
	s, r, catalog := registryFixture(t)
	configurations := map[string]sshinventory.Config{}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	key, _ := ssh.NewPublicKey(pub)
	for i := 0; i < 9; i++ {
		name := fmt.Sprintf("owner%d", i)
		dir := protectedTestDirectory(t)
		os.Chmod(dir, 0700)
		sc, sk := cert(t, int64(i*2+10))
		cc, ck := cert(t, int64(i*2+11))
		for n, b := range map[string][]byte{"owner-server.crt": sc, "owner-server.key": sk, "owner-client.crt": cc, "owner-client.key": ck, "controller-ca.crt": sc} {
			os.WriteFile(filepath.Join(dir, n), b, 0600)
		}
		catalog.Identities[name] = nativebundle.RegistrationIdentity{Directory: dir, Template: r.Template, Administrator: r.Administrator, HostPrefix: "host-"}
		configurations[fmt.Sprintf("target%d", i)] = sshinventory.Config{Address: "127.0.0.1", Port: 22, User: "inventory", HostKey: string(ssh.MarshalAuthorizedKey(key)), CredentialRef: "inventory"}
	}
	compiler, e := nativebundle.NewRegistrationCompiler(catalog)
	if e != nil {
		t.Fatal(e)
	}
	inventory, e := sshinventory.NewService(configurations, func(context.Context, string) (ssh.Signer, error) { t.Fatal("network credential"); return signer, nil }, func(context.Context, string, string, string, string, bool) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	s, e = NewService(compiler, inventory, s.directory)
	if e != nil {
		t.Fatal(e)
	}
	targets := inventory.Targets()
	for i := 0; i < 9; i++ {
		q := r
		q.Revision = compiler.Revision()
		q.Identity = fmt.Sprintf("owner%d", i)
		q.PreparationID = fmt.Sprintf("%064x", i+1)
		q.Host = fmt.Sprintf("host-%d", i)
		q.InventoryTargetID = targets[i].ID
		q.InventoryTargetDigest = nativeplanning.Digest(targets[i])
		review, e := s.Review(t.Context(), q)
		if e != nil {
			t.Fatal(e)
		}
		_, e = s.Register(t.Context(), q, review.RegistrationDigest)
		if i < 8 && e != nil {
			t.Fatal(i, e)
		}
		if i == 8 {
			if e == nil {
				t.Fatal("ninth target materialized")
			}
			if _, e = os.Stat(filepath.Join(catalog.PrivateParent, q.PreparationID)); !os.IsNotExist(e) {
				t.Fatal("ninth output")
			}
		}
	}
	// An administrator cannot silently discard old identity history by changing
	// catalog references while retaining this registry.
	changed := catalog.Templates[r.Template]
	changed.ArtifactReference = "different"
	catalog.Templates[r.Template] = changed
	compiler, e = nativebundle.NewRegistrationCompiler(catalog)
	if e != nil {
		t.Fatal(e)
	}
	reopened, e := NewService(compiler, inventory, s.directory)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = reopened.Restore(t.Context()); e == nil {
		t.Fatal("catalog history silently rebound")
	}
}

// Registry state and materialized planning paths require protected ancestors.
func protectedTestDirectory(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	p, err := os.MkdirTemp(cwd, ".registration-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(p) })
	return p
}
