//go:build linux

package nativeplanning

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
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
func actualInventory(t *testing.T) (*sshinventory.Service, Binding) {
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
				return nil, ErrSelection
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
	binding := Binding{PreparationID: strings.Repeat("a", 64), Host: "host-one", InventoryTarget: inventory.Targets()[0], NativeTarget: nativeprovision.Target{Address: "192.0.2.1", Port: 22, AdminUser: "administrator", HostKey: string(ssh.MarshalAuthorizedKey(admin.PublicKey())), AdminKeyFingerprint: ssh.FingerprintSHA256(admin.PublicKey())}}
	return inventory, binding
}
func TestActualDiscoveryToSavedSelectionAndNoAuthority(t *testing.T) {
	inventory, binding := actualInventory(t)
	cwd, _ := os.Getwd()
	dir, e := os.MkdirTemp(cwd, ".selection-test-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	s, e := NewService(inventory, map[string]Binding{"host": binding}, dir)
	if e != nil {
		t.Fatal(e)
	}
	p, e := s.Probe(context.Background(), "host", Digest(binding), "11111111-1111-4111-8111-111111111111")
	if e != nil {
		t.Fatal(e)
	}
	if *p.Observation.LogicalCPUs != 8 || p.Defaults.ReserveRAMMiB < 2048 || p.Defaults.Limits.MemoryBytes/MiB > *p.Observation.AvailableRAMMiB-p.Defaults.ReserveRAMMiB {
		t.Fatal(p)
	}
	// Returned proposal copies cannot rewrite the retained authenticated observation.
	*p.Observation.RAMMiB = 999999
	bad := p.Defaults
	bad.Limits.MemoryBytes = 1 << 60
	if _, e = s.Save(context.Background(), "host", p.ReviewDigest, bad); e == nil {
		t.Fatal("overbudget")
	}
	if _, e = s.Save(context.Background(), "host", "wrong", p.Defaults); e == nil {
		t.Fatal("wrong review")
	}
	saved, e := s.Save(context.Background(), "host", p.ReviewDigest, p.Defaults)
	if e != nil || saved.State != "saved_configuration_not_applied" || ValidateFreshSelection(saved.Selection, time.Now()) != nil || saved.Selection.PhysicalCapacityQualified || *saved.Selection.Observation.RAMMiB != 16384 {
		t.Fatal(saved, e)
	}
	mix, e := s.ProposeMix(context.Background(), "host", saved.SelectionDigest, 8192)
	if e != nil || mix.Host != binding.Host || mix.SelectionDigest != saved.SelectionDigest || mix.DiskFitVerified || len(mix.Entries) == 0 {
		t.Fatal(mix, e)
	}
	if _, e = s.ProposeMix(context.Background(), "host", saved.SelectionDigest, 0); e == nil {
		t.Fatal("missing explicit disk accepted")
	}
	if _, e = s.ProposeMix(context.Background(), "host", strings.Repeat("f", 64), 8192); e == nil {
		t.Fatal("unrecorded selection accepted")
	}
	names, _ := os.ReadDir(dir)
	if len(names) != 1 {
		t.Fatal("unexpected persisted files")
	}
	raw, e := os.ReadFile(dir + "/" + names[0].Name())
	if e != nil {
		t.Fatal(e)
	}
	var actual Selection
	if json.Unmarshal(raw, &actual) != nil || Digest(actual) != saved.SelectionDigest {
		t.Fatal("wrong durable config")
	}
	aged := actual
	aged.Observation.ObservedAt = aged.Observation.ObservedAt.Add(-2 * Lifetime)
	aged.ObservationDigest = ObservationDigest(aged.Binding, aged.Observation)
	aged.SavedAt = aged.SavedAt.Add(-2 * Lifetime)
	aged.ExpiresAt = aged.ExpiresAt.Add(-2 * Lifetime)
	if ValidateSelection(aged) != nil {
		t.Fatal("aged fixture invalid")
	}
	agedRaw, _ := json.Marshal(aged)
	agedDigest := Digest(aged)
	if e = os.WriteFile(dir+"/selection-"+binding.PreparationID+"-"+agedDigest+".json", agedRaw, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ProposeMix(context.Background(), "host", agedDigest, 8192); e == nil {
		t.Fatal("expired saved selection proposed")
	}
	item := s.cache["host"]
	item.began = time.Now().Add(-Lifetime)
	s.cache["host"] = item
	if _, e = s.Save(context.Background(), "host", p.ReviewDigest, p.Defaults); e == nil {
		t.Fatal("expired review")
	}
	future := saved.Selection
	future.SavedAt = time.Now().Add(time.Minute)
	if ValidateFreshSelection(future, time.Now()) == nil {
		t.Fatal("future save")
	}
}
func TestSavedHistoryRejectsMalformedAndOrphanFile(t *testing.T) {
	inventory, binding := actualInventory(t)
	cwd, _ := os.Getwd()
	dir, e := os.MkdirTemp(cwd, ".selection-history-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	s, e := NewService(inventory, map[string]Binding{"host": binding}, dir)
	if e != nil {
		t.Fatal(e)
	}
	if v, e := s.SavedSelections("host"); e != nil || len(v) != 0 {
		t.Fatal(v, e)
	}
	name := dir + "/selection-" + binding.PreparationID + "-" + strings.Repeat("b", 64) + ".json"
	os.WriteFile(name, []byte("{}"), 0600)
	if _, e = s.SavedSelections("host"); e == nil {
		t.Fatal("unbound file accepted")
	}
}
