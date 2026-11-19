//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/capsulepeer"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecapsule"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeegress"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativequota"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativezfs"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
)

func capsuleFixture() capsuleConfiguration {
	h := strings.Repeat("a", 64)
	files := map[string]string{"bin/Runner.Listener": h, "bin/Runner.Listener.dll": h}
	raw, _ := json.Marshal(files)
	sum := sha256.Sum256(raw)
	image := protectedboot.ImageDescriptor{InitSHA256: h, LauncherSHA256: h, BundleDigest: hex.EncodeToString(sum[:]), RunnerRelease: "2.321.0", UID: 1001, GID: 1001, Files: files}
	return capsuleConfiguration{
		Artifacts:      nativezfs.ArtifactConfiguration{Binary: "/usr/sbin/zfs", BinarySHA256: h, Helper: "/usr/libexec/zfs-exec", HelperSHA256: h, ParentDataset: "tank/artifacts", ParentGUID: "2", QuotaBytes: 1 << 30, Image: "/private/image", ImageSHA256: h},
		Control:        controlConfig{PoolReader: nativequota.Configuration{Binary: "/usr/sbin/zpool", BinarySHA256: h, Helper: "/usr/libexec/quota", HelperSHA256: h}, Listen: "127.0.0.1:9443", ControllerURL: "https://controller.example", ControllerClientLeaf: h, ControllerServerLeaf: h, ServerCertificate: "/private/server.pem", ServerKey: "/private/server.key", OwnerCertificate: "/private/owner.pem", OwnerKey: "/private/owner.key", ControllerCA: "/private/ca.pem", Policy: nativeapi.PolicySpec{Version: nativeapi.PartitionPolicyVersion, Controller: "controller", Host: "host", NativeSHA256: h, LauncherSHA256: h, ServerVersion: nativeapi.NativeVersion, Pools: map[string]nativeapi.Pool{"pool": {Flavor: "small", Image: "runner", Fingerprint: h, Type: "container", Arch: "amd64", StoragePool: "private", Bridge: "private", ExpectedProfiles: []string{"runner"}, ProtectedImage: &image}}}},
		Limits:         nativeboundary.Limits{MemoryBytes: 1 << 30, CPUMilli: 1000, Tasks: 512},
		Partition:      capsulepeer.PartitionSpec{Bootstrap: nativeboundary.Limits{MemoryBytes: 1 << 27, CPUMilli: 100, Tasks: 32}, Daemon: nativeboundary.Limits{MemoryBytes: 1 << 28, CPUMilli: 100, Tasks: 64}, Monitors: nativeboundary.Limits{MemoryBytes: 1 << 28, CPUMilli: 100, Tasks: 64}, Guests: nativeboundary.Limits{MemoryBytes: 3 << 27, CPUMilli: 700, Tasks: 352}},
		StorageBudgets: nativezfs.CategoryBudgets{Runtime: 1 << 20, Categories: map[string]int64{"buckets": 1 << 20, "containers": 1 << 20, "custom": 1 << 20, "images": 1 << 20, "virtual-machines": 1 << 20, "deleted/buckets": 1 << 20, "deleted/containers": 1 << 20, "deleted/custom": 1 << 20, "deleted/images": 1 << 20, "deleted/virtual-machines": 1 << 20}},
		Launch:         nativecapsule.Configuration{Nspawn: "/usr/bin/systemd-nspawn", NspawnSHA256: h, Launcher: "/usr/libexec/capsule-launch", LauncherSHA256: h, ImageSHA256: h, StageSHA256: h},
		Storage:        nativezfs.Configuration{Binary: "/usr/sbin/zfs", BinarySHA256: h, Helper: "/usr/libexec/zfs-exec", HelperSHA256: h, RootDataset: "tank/native", RootGUID: "1", QuotaBytes: 1 << 30},
		Network:        nativeegress.Specification{Mode: nativeegress.PublicInternet, TransitCIDR: "10.240.0.0/30", RunnerCIDR: "10.241.0.0/24", DNS: []string{"1.1.1.1"}, ControlPlaneCIDRs: []string{"8.8.8.8/32"}},
		NetworkTools:   nativeegress.Configuration{IP: "/usr/sbin/ip", IPSHA256: h, NFT: "/usr/sbin/nft", NFTSHA256: h, Helper: "/usr/libexec/egress", HelperSHA256: h},
		QuotaReader:    nativequota.Configuration{Binary: "/usr/sbin/zfs", BinarySHA256: h, Helper: "/usr/libexec/quota-read", HelperSHA256: h}, RuntimeQuotaHelperSHA256: h, DevicePinRoot: "/sys/fs/bpf/native",
	}
}
func TestCapsuleConfigurationValidatesTemplateWithoutMintingIdentity(t *testing.T) {
	c := capsuleFixture()
	if e := validateCapsuleConfiguration(c); e != nil {
		t.Fatal(e)
	}
	if c.Control.Policy.DomainID != "" || c.Control.Policy.EgressDigest != "" || c.Control.Policy.Pools["pool"].ZFSDataset != "" {
		t.Fatal("template validation mutated caller identity")
	}
	for name, change := range map[string]func(*capsuleConfiguration){
		"missing-artifacts":       func(c *capsuleConfiguration) { c.Artifacts = nativezfs.ArtifactConfiguration{} },
		"caller-image":            func(c *capsuleConfiguration) { c.Launch.Image = "/caller/image" },
		"caller-id-registry":      func(c *capsuleConfiguration) { c.Launch.Registry = "/caller/ids" },
		"caller-state":            func(c *capsuleConfiguration) { c.Launch.StateParent = "/caller/state" },
		"caller-zfs-registry":     func(c *capsuleConfiguration) { c.Storage.Registry = "/caller/zfs" },
		"caller-egress-registry":  func(c *capsuleConfiguration) { c.NetworkTools.Registry = "/caller/egress" },
		"caller-devices":          func(c *capsuleConfiguration) { c.DeviceRegistry = "/caller/devices" },
		"artifact-image-mismatch": func(c *capsuleConfiguration) { c.Artifacts.ImageSHA256 = strings.Repeat("b", 64) },
		"artifact-tool-mismatch":  func(c *capsuleConfiguration) { c.Artifacts.Helper = "/other/helper" },
		"unlimited-artifacts":     func(c *capsuleConfiguration) { c.Artifacts.QuotaBytes = 0 },
		"missing-pool-reader":     func(c *capsuleConfiguration) { c.Control.PoolReader = nativequota.Configuration{} },
		"relative-pool-reader":    func(c *capsuleConfiguration) { c.Control.PoolReader.Binary = "zpool" },
		"unpinned-pool-reader":    func(c *capsuleConfiguration) { c.Control.PoolReader.BinarySHA256 = "" },

		"missing-partition": func(c *capsuleConfiguration) { c.Partition = capsulepeer.PartitionSpec{} },
		"partition-overflow": func(c *capsuleConfiguration) {
			c.Partition.Daemon.MemoryBytes = 1<<63 - 1
			c.Partition.Guests.MemoryBytes = 1<<63 - 1
		},
		"missing-storage-budget":  func(c *capsuleConfiguration) { delete(c.StorageBudgets.Categories, "deleted/images") },
		"storage-budget-overflow": func(c *capsuleConfiguration) { c.StorageBudgets.Runtime = 1<<63 - 1 },
		"storage-budget-unknown": func(c *capsuleConfiguration) {
			delete(c.StorageBudgets.Categories, "images")
			c.StorageBudgets.Categories["other"] = 1
		},

		"shared-transit-bridge": func(c *capsuleConfiguration) {
			p := c.Control.Policy.Pools["pool"]
			p.Bridge = "garm0"
			c.Control.Policy.Pools["pool"] = p
		},
		"different-storage-pool": func(c *capsuleConfiguration) {
			p := c.Control.Policy.Pools["pool"]
			p.StoragePool = "second"
			c.Control.Policy.Pools["other"] = p
		},
		"different-runner-bridge": func(c *capsuleConfiguration) {
			p := c.Control.Policy.Pools["pool"]
			p.Bridge = "second"
			c.Control.Policy.Pools["other"] = p
		},
		"caller-domain":        func(c *capsuleConfiguration) { c.Control.Policy.DomainID = strings.Repeat("a", 64) },
		"caller-network-birth": func(c *capsuleConfiguration) { c.Control.Policy.EgressDigest = strings.Repeat("a", 64) },
		"caller-dataset": func(c *capsuleConfiguration) {
			p := c.Control.Policy.Pools["pool"]
			p.ZFSDataset = "tank/adopted"
			c.Control.Policy.Pools["pool"] = p
		},
		"legacy-policy":          func(c *capsuleConfiguration) { c.Control.Policy.Version = nativeapi.ProtectedPolicyVersion },
		"different-stage":        func(c *capsuleConfiguration) { c.Launch.StageSHA256 = strings.Repeat("b", 64) },
		"different-quota-helper": func(c *capsuleConfiguration) { c.RuntimeQuotaHelperSHA256 = strings.Repeat("b", 64) },
		"different-zfs":          func(c *capsuleConfiguration) { c.QuotaReader.BinarySHA256 = strings.Repeat("b", 64) },
		"relative-registry":      func(c *capsuleConfiguration) { c.DeviceRegistry = "relative" },
		"credential-url":         func(c *capsuleConfiguration) { c.Control.ControllerURL = "https://secret@controller.example" },
		"zero-guid":              func(c *capsuleConfiguration) { c.Storage.RootGUID = "0" },
		"unlimited-memory":       func(c *capsuleConfiguration) { c.Limits.MemoryBytes = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			c := capsuleFixture()
			change(&c)
			if validateCapsuleConfiguration(c) == nil {
				t.Fatal("invalid capsule accepted")
			}
		})
	}
}
func TestCapsuleCommandStillRefusesBeforeAnyReadOrHostEffect(t *testing.T) {
	if e := run([]string{"--capsule-config=/nonexistent/config.json"}, io.Discard); e != errConfinement {
		t.Fatal("activation not refused first", e)
	}
	if e := run([]string{"--capsule-config=/nonexistent/config.json", "--domain-dir=/tmp/old"}, io.Discard); e != errOwner {
		t.Fatal("mixed old/new owner accepted", e)
	}
	if serveConfiguredCapsule(nil, "/missing") == nil {
		t.Fatal("nil context")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if serveConfiguredCapsule(ctx, "/missing") == nil {
		t.Fatal("canceled construction")
	}
	if serveCapsuleListener(context.Background(), "127.0.0.1:0", nil, nil) == nil {
		t.Fatal("listener without concrete native authority")
	}
}

func TestCapsuleRolesCannotShareLeafIdentity(t *testing.T) {
	leaves := []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)}
	if !distinctCapsuleRoles(leaves...) {
		t.Fatal("distinct roles rejected")
	}
	for i := 0; i < len(leaves); i++ {
		for j := i + 1; j < len(leaves); j++ {
			copyLeaves := append([]string(nil), leaves...)
			copyLeaves[j] = copyLeaves[i]
			if distinctCapsuleRoles(copyLeaves...) {
				t.Fatal("role overlap accepted", i, j)
			}
		}
	}
}
