//go:build linux

package nativebundle

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/capsulepeer"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeplanning"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/crypto/ssh"
)

func selectionFixture(t *testing.T, c *Configuration, cfg map[string]any) nativeplanning.Selection {
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
func TestBuildAppliesOnlyBoundSelectedResources(t *testing.T) {
	c, cfg := fixture(t)
	s := selectionFixture(t, &c, cfg)
	c.ExpectedSelectionDigest = nativeplanning.Digest(s)
	r, e := Build(context.Background(), c)
	if e != nil {
		t.Fatal(e)
	}
	if r.SelectionDigest != nativeplanning.Digest(s) || r.ActivationAuthorized {
		t.Fatal(r)
	}
	raw, e := os.ReadFile(filepath.Join(c.OutputDirectory, "capsule.json"))
	if e != nil {
		t.Fatal(e)
	}
	var got struct {
		Limits    nativeboundary.Limits
		Partition capsulepeer.PartitionSpec
		Storage   struct {
			Quota json.Number `json:"quota_bytes"`
		}
	}
	if json.Unmarshal(raw, &got) != nil || got.Limits != s.Limits || got.Partition != s.Partition || got.Storage.Quota.String() != "9007199254740993" {
		t.Fatal("selected values or storage changed")
	}
}
func TestBuildSelectionFailsBeforeOutput(t *testing.T) {
	for _, mode := range []string{"selection-digest", "id", "host", "target", "review", "inventory-address", "expired", "future", "future-saved", "overbudget", "partition", "qualified", "missing-pair", "duplicate", "unknown", "mode"} {
		t.Run(mode, func(t *testing.T) {
			c, cfg := fixture(t)
			s := selectionFixture(t, &c, cfg)
			switch mode {
			case "selection-digest":
				c.ExpectedSelectionDigest = strings.Repeat("f", 64)
			case "id":
				s.Binding.PreparationID = strings.Repeat("d", 64)
			case "host":
				cfg["control"].(map[string]any)["policy"].(map[string]any)["host"] = "other"
				writeConfig(t, c, cfg)
			case "target":
				s.Binding.NativeTarget.Port = 2222
			case "review":
				s.Binding.InventoryTarget.ReviewDigest = strings.Repeat("e", 64)
			case "inventory-address":
				s.Binding.InventoryTarget.Address = "not-an-ip"
			case "expired":
				s.Observation.ObservedAt = time.Now().Add(-time.Hour)
				s.SavedAt = s.Observation.ObservedAt
				s.ExpiresAt = s.Observation.ObservedAt.Add(nativeplanning.Lifetime)
			case "future":
				s.Observation.ObservedAt = time.Now().Add(time.Hour)
				s.SavedAt = s.Observation.ObservedAt
				s.ExpiresAt = s.Observation.ObservedAt.Add(nativeplanning.Lifetime)
			case "future-saved":
				s.SavedAt = time.Now().Add(time.Minute)
			case "overbudget":
				s.Limits.CPUMilli = 9000
			case "partition":
				s.Partition.Guests.MemoryBytes = 8 << 30
			case "qualified":
				s.PhysicalCapacityQualified = true
			case "missing-pair":
				c.ExpectedBindingFile = ""
			}
			s.ObservationDigest = nativeplanning.ObservationDigest(s.Binding, s.Observation)
			saveSelection(t, c.SelectionFile, s)
			if mode == "duplicate" {
				os.WriteFile(c.SelectionFile, []byte("{\"version\":1,\"VERSION\":2}"), 0600)
			}
			if mode == "unknown" {
				os.WriteFile(c.SelectionFile, []byte("{\"unknown\":true}"), 0600)
			}
			if mode == "mode" {
				os.Chmod(c.SelectionFile, 0644)
			}
			if _, e := Build(context.Background(), c); e == nil {
				t.Fatal("accepted invalid selection")
			}
			if _, e := os.Lstat(c.OutputDirectory); !os.IsNotExist(e) {
				t.Fatal("created output on rejection")
			}
		})
	}
}
