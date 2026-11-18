//go:build linux

package nativeassembly

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
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/capsulepeer"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativebundle"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeplanning"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprepare"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/crypto/ssh"
)

func key(t *testing.T) ssh.Signer {
	t.Helper()
	_, p, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	s, e := ssh.NewSignerFromKey(p)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func actualInventory(t *testing.T) (*sshinventory.Service, nativeplanning.Binding) {
	t.Helper()
	address := "127.0.0.1"
	listener, e := net.Listen("tcp", net.JoinHostPort(address, "0"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { listener.Close() })
	host, credential, admin := key(t), key(t), key(t)
	go func() {
		raw, e := listener.Accept()
		if e != nil {
			return
		}
		defer raw.Close()
		raw.SetDeadline(time.Now().Add(5 * time.Second))
		cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if string(k.Marshal()) != string(credential.PublicKey().Marshal()) {
				return nil, nativeplanning.ErrSelection
			}
			return nil, nil
		}}
		cfg.AddHostKey(host)
		conn, channels, requests, e := ssh.NewServerConn(raw, cfg)
		if e != nil {
			return
		}
		defer conn.Close()
		go ssh.DiscardRequests(requests)
		for ch := range channels {
			stream, requests, e := ch.Accept()
			if e != nil {
				return
			}
			defer stream.Close()
			for req := range requests {
				var command struct{ Command string }
				if req.Type != "exec" || ssh.Unmarshal(req.Payload, &command) != nil || command.Command != "/bin/sh -s" {
					req.Reply(false, nil)
					return
				}
				req.Reply(true, nil)
				script, e := io.ReadAll(io.LimitReader(stream, 8193))
				if e != nil || len(script) > 8192 {
					return
				}
				io.WriteString(stream, "garm_inventory_v1\ncpu_online=8\nram_kib=16777216\navailable_ram_kib=12582912\nroot_disk_kib=104857600\nroot_available_kib=52428800\n")
				stream.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				return
			}
		}
	}()
	_, portstr, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portstr)
	config := sshinventory.Config{Address: address, Port: uint16(port), User: "inventory", HostKey: string(ssh.MarshalAuthorizedKey(host.PublicKey())), CredentialRef: "restricted"}
	audited := false
	inventory, e := sshinventory.NewService(map[string]sshinventory.Config{"one": config}, func(context.Context, string) (ssh.Signer, error) {
		if !audited {
			t.Error("credentials before audit")
		}
		return credential, nil
	}, func(_ context.Context, _ string, _ string, _ string, phase string, _ bool) error {
		if phase == "requested" {
			audited = true
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	binding := nativeplanning.Binding{PreparationID: strings.Repeat("a", 64), Host: "host-one", InventoryTarget: inventory.Targets()[0], NativeTarget: nativeprovision.Target{Address: "192.0.2.1", Port: 22, AdminUser: "administrator", HostKey: string(ssh.MarshalAuthorizedKey(admin.PublicKey())), AdminKeyFingerprint: ssh.FingerprintSHA256(admin.PublicKey())}}
	return inventory, binding
}
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

func fixture(t *testing.T) (*Service, *nativeplanning.Service, map[string]Recipe, nativeplanning.Saved) {
	t.Helper()
	inventory, binding := actualInventory(t)
	cwd, _ := os.Getwd()
	root, e := os.MkdirTemp(cwd, ".assembly-test-")
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
	savedDir, output, export, private := dir("saved"), dir("output"), dir("export"), dir("private")
	planning, e := nativeplanning.NewService(inventory, map[string]nativeplanning.Binding{"host": binding}, savedDir)
	if e != nil {
		t.Fatal(e)
	}
	proposal, e := planning.Probe(context.Background(), "host", nativeplanning.Digest(binding), "11111111-1111-4111-8111-111111111111")
	if e != nil {
		t.Fatal(e)
	}
	saved, e := planning.Save(context.Background(), "host", proposal.ReviewDigest, proposal.Defaults)
	if e != nil {
		t.Fatal(e)
	}
	checksum := ""
	for _, n := range nativeprepare.ExecutableNames() {
		raw := elfFixture()
		h := sha256.Sum256(raw)
		checksum += hex.EncodeToString(h[:]) + "  " + n + "\n"
		if os.WriteFile(filepath.Join(export, n), raw, 0555) != nil {
			t.Fatal("artifact")
		}
	}
	os.WriteFile(filepath.Join(export, "SHA256SUMS"), []byte(checksum), 0444)
	pin := sha256.Sum256([]byte(checksum))
	sc, sk := cert(t, 1)
	cc, ck := cert(t, 2)
	for n, raw := range map[string][]byte{"owner-server.crt": sc, "owner-server.key": sk, "owner-client.crt": cc, "owner-client.key": ck, "controller-ca.crt": sc} {
		os.WriteFile(filepath.Join(private, n), raw, 0600)
	}
	cfg := map[string]any{"control": map[string]any{"policy": map[string]any{"version": "garm-native-policy/v5", "host": binding.Host}, "controller_client_leaf": strings.Repeat("a", 64), "controller_server_leaf": strings.Repeat("b", 64), "pool_reader": map[string]any{}}, "launch": map[string]any{}, "storage": map[string]any{}, "artifacts": map[string]any{}, "network_tools": map[string]any{}, "quota_reader": map[string]any{}}
	raw, _ := json.Marshal(cfg)
	os.WriteFile(filepath.Join(private, "capsule.json"), raw, 0600)
	raw, _ = json.Marshal(binding)
	expected := filepath.Join(private, "binding.json")
	os.WriteFile(expected, raw, 0600)
	recipes := map[string]Recipe{"host": {Binding: binding, OutputParent: output, Build: nativebundle.Configuration{ExportDirectory: export, ExportManifestSHA256: hex.EncodeToString(pin[:]), PrivateDirectory: private, ID: binding.PreparationID, Architecture: "amd64", ExpectedBindingFile: expected}}}
	service, e := NewService(planning, recipes)
	if e != nil {
		t.Fatal(e)
	}
	return service, planning, recipes, saved
}

func TestActualSSHDiscoverySavedChoiceBuildAndHistoricalInspection(t *testing.T) {
	s, p, recipes, saved := fixture(t)
	ctx := context.Background()
	if _, e := s.InspectCompleted(ctx, "host", saved.SelectionDigest); e == nil {
		t.Fatal("inspected absent output")
	}
	before, _ := os.ReadDir(recipes["host"].OutputParent)
	if len(before) != 0 {
		t.Fatal("inspection wrote files")
	}
	c, e := s.Build(ctx, "host", saved.SelectionDigest)
	if e != nil {
		t.Fatal(e)
	}
	report := c.Report()
	if report.State != "bundle_created_not_reloaded" || report.ActivationAuthorized || report.SelectionDigest != saved.SelectionDigest || !hash(report.AssemblyDigest) || c.Target() != recipes["host"].Binding.NativeTarget {
		t.Fatal("incorrect report")
	}
	raw, e := os.ReadFile(filepath.Join(c.BundlePath(), "capsule.json"))
	if e != nil {
		t.Fatal(e)
	}
	var actual struct {
		Limits    nativeboundary.Limits
		Partition capsulepeer.PartitionSpec
	}
	if json.Unmarshal(raw, &actual) != nil || actual.Limits != saved.Selection.Limits || actual.Partition != saved.Selection.Partition {
		t.Fatal("wrong actual selected resources")
	}
	if _, e = s.Build(ctx, "host", saved.SelectionDigest); e == nil {
		t.Fatal("replayed assembly")
	}
	path, _, e := p.LookupSaved(ctx, "host", saved.SelectionDigest)
	if e != nil {
		t.Fatal(e)
	}
	os.Remove(path)
	// Completed configuration is independently verifiable after discovery expires
	// or its historical source is unavailable. It is not fresh capacity evidence.
	reopened, e := NewService(p, recipes)
	if e != nil {
		t.Fatal(e)
	}
	inspected, e := reopened.InspectCompleted(ctx, "host", saved.SelectionDigest)
	if e != nil || inspected.Report() != report {
		t.Fatal("historical inspection", e)
	}
	os.WriteFile(filepath.Join(c.BundlePath(), "capsule.json"), []byte("{}"), 0600)
	if _, e = reopened.InspectCompleted(ctx, "host", saved.SelectionDigest); e == nil {
		t.Fatal("accepted changed bundle bytes")
	}
}

func TestReceiptManifestMismatchAndUnknownCannotAdopt(t *testing.T) {
	for _, mode := range []string{"receipt", "partial"} {
		t.Run(mode, func(t *testing.T) {
			s, _, recipes, saved := fixture(t)
			r := recipes["host"]
			if mode == "partial" {
				os.Remove(filepath.Join(r.Build.ExportDirectory, "capacity-native-prepare"))
			}
			_, e := s.Build(context.Background(), "host", saved.SelectionDigest)
			if mode == "partial" {
				if e == nil {
					t.Fatal("missing artifact accepted")
				}
				// Repair the input, not the consumed output. Only the durable
				// intent must prevent a second builder execution now.
				if os.WriteFile(filepath.Join(r.Build.ExportDirectory, "capacity-native-prepare"), elfFixture(), 0555) != nil {
					t.Fatal("repair fixture")
				}
				if _, e = s.Build(context.Background(), "host", saved.SelectionDigest); e == nil {
					t.Fatal("partial output rebuilt")
				}
			} else {
				if e != nil {
					t.Fatal(e)
				}
				path := filepath.Join(r.OutputParent, r.stem(saved.SelectionDigest)+".receipt.json")
				receipt, e := readRecord(path)
				if e != nil {
					t.Fatal(e)
				}
				receipt.Report.ManifestDigest = strings.Repeat("f", 64)
				receipt.Report.AssemblyDigest = assemblyDigest(receipt)
				raw, _ := json.Marshal(receipt)
				os.WriteFile(path, raw, 0600)
			}
			if _, e = s.InspectCompleted(context.Background(), "host", saved.SelectionDigest); e == nil {
				t.Fatal("inconsistent or partial receipt accepted")
			}
		})
	}
}
func TestConcurrentAssemblyOneOutputAndCancelledNoEffect(t *testing.T) {
	s, p, recipes, saved := fixture(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := s.Build(cancelled, "host", saved.SelectionDigest); e == nil {
		t.Fatal("cancellation")
	}
	names, _ := os.ReadDir(recipes["host"].OutputParent)
	if len(names) != 0 {
		t.Fatal("cancel wrote")
	}
	other, e := NewService(p, recipes)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v := s
			if i%2 == 0 {
				v = other
			}
			if _, e := v.Build(context.Background(), "host", saved.SelectionDigest); e == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if success != 1 {
		t.Fatal("not one output", success)
	}
	if _, e = s.InspectCompleted(context.Background(), "host", saved.SelectionDigest); e != nil {
		t.Fatal(e)
	}
}
