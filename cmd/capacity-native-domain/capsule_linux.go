//go:build linux

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/capsulepeer"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecapsule"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecontrol"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativedevice"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeegress"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativequota"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativezfs"
)

// capsuleConfiguration describes inputs, never observed authority. DomainID,
// per-pool dataset and EgressDigest are derived only from actual fresh births.
// The command refuses this mode before loading this configuration until the
// independent execution boundary is qualified. No enable flag overrides it.
type capsuleConfiguration struct {
	Artifacts      nativezfs.ArtifactConfiguration `json:"artifacts"`
	Partition      capsulepeer.PartitionSpec       `json:"partition"`
	StorageBudgets nativezfs.CategoryBudgets       `json:"storage_budgets"`

	Control                  controlConfig               `json:"control"`
	Limits                   nativeboundary.Limits       `json:"limits"`
	Launch                   nativecapsule.Configuration `json:"launch"`
	Storage                  nativezfs.Configuration     `json:"storage"`
	Network                  nativeegress.Specification  `json:"network"`
	NetworkTools             nativeegress.Configuration  `json:"network_tools"`
	QuotaReader              nativequota.Configuration   `json:"quota_reader"`
	RuntimeQuotaHelperSHA256 string                      `json:"runtime_quota_helper_sha256"`
	DeviceRegistry           string                      `json:"device_registry"`
	DevicePinRoot            string                      `json:"device_pin_root"`
}

func loadCapsuleControl(path string) (capsuleConfiguration, error) {
	var c capsuleConfiguration
	raw, err := rootData(path, true, 4<<20)
	if err != nil {
		return c, err
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return c, errOwner
	}
	return c, validateCapsuleConfiguration(c)
}
func capsuleHash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func validateCapsuleConfiguration(c capsuleConfiguration) error {
	if nativezfs.ValidateArtifactConfiguration(c.Artifacts) != nil || c.Partition.Validate(c.Limits) != nil || nativezfs.ValidateCategoryBudgets(c.Storage.QuotaBytes, c.StorageBudgets) != nil {
		return errOwner
	}

	if c.Control.Policy.Version != nativeapi.PartitionPolicyVersion || c.Control.Policy.DomainID != "" || c.Control.Policy.EgressDigest != "" || c.Control.Policy.LauncherSHA256 != c.Launch.StageSHA256 || c.Limits.MemoryBytes <= 0 || c.Limits.CPUMilli <= 0 || c.Limits.Tasks <= 0 || c.Storage.QuotaBytes <= 0 {
		return errOwner
	}
	// These paths are exclusively produced by the live host artifact root.
	if c.Launch.Image != "" || c.Launch.Registry != "" || c.Launch.StateParent != "" || c.Storage.Registry != "" || c.NetworkTools.Registry != "" || c.DeviceRegistry != "" ||
		c.Artifacts.ImageSHA256 != c.Launch.ImageSHA256 || c.Artifacts.Binary != c.Storage.Binary || c.Artifacts.BinarySHA256 != c.Storage.BinarySHA256 || c.Artifacts.Helper != c.Storage.Helper || c.Artifacts.HelperSHA256 != c.Storage.HelperSHA256 {
		return errOwner
	}
	if c.RuntimeQuotaHelperSHA256 != c.QuotaReader.HelperSHA256 || c.Storage.BinarySHA256 != c.QuotaReader.BinarySHA256 {
		return errOwner
	}
	endpoint, err := url.Parse(c.Control.ControllerURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "" {
		return errOwner
	}
	guid, err := strconv.ParseUint(c.Storage.RootGUID, 10, 64)
	if err != nil || guid == 0 || strconv.FormatUint(guid, 10) != c.Storage.RootGUID {
		return errOwner
	}
	for _, p := range []string{c.Launch.Nspawn, c.Launch.Launcher, c.Storage.Binary, c.Storage.Helper, c.NetworkTools.IP, c.NetworkTools.NFT, c.NetworkTools.Helper, c.QuotaReader.Binary, c.QuotaReader.Helper, c.Control.PoolReader.Binary, c.Control.PoolReader.Helper, c.DevicePinRoot, c.Control.ServerCertificate, c.Control.ServerKey, c.Control.OwnerCertificate, c.Control.OwnerKey, c.Control.ControllerCA} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return errOwner
		}
	}
	for _, h := range []string{c.Launch.NspawnSHA256, c.Launch.LauncherSHA256, c.Launch.ImageSHA256, c.Launch.StageSHA256, c.Storage.BinarySHA256, c.Storage.HelperSHA256, c.NetworkTools.IPSHA256, c.NetworkTools.NFTSHA256, c.NetworkTools.HelperSHA256, c.QuotaReader.BinarySHA256, c.QuotaReader.HelperSHA256, c.Control.PoolReader.BinarySHA256, c.Control.PoolReader.HelperSHA256, c.RuntimeQuotaHelperSHA256, c.Control.ControllerClientLeaf, c.Control.ControllerServerLeaf} {
		if !capsuleHash(h) {
			return errOwner
		}
	}
	host, port, e := net.SplitHostPort(c.Control.Listen)
	n, pe := strconv.Atoi(port)
	if e != nil || net.ParseIP(host) == nil || pe != nil || n < 1 || n > 65535 {
		return errOwner
	}
	network, e := nativeegress.Compile(c.Network)
	if e != nil {
		return e
	}
	// Shape checking only. This temporary policy is never returned, stored,
	// enrolled or sent to an owner. Actual identity is filled after kernel birth.
	spec := c.Control.Policy
	spec.DomainID = strings.Repeat("0", 64)
	spec.EgressDigest = network.Digest()
	spec.Pools = make(map[string]nativeapi.Pool, len(c.Control.Policy.Pools))
	storagePool, bridge := "", ""
	for id, pool := range c.Control.Policy.Pools {
		if pool.ZFSDataset != "" {
			return errOwner
		}
		if len(pool.Bridge) > 15 || pool.Bridge == "lo" || pool.Bridge == "none" || pool.Bridge == "garm0" {
			return errOwner
		}
		if storagePool != "" && storagePool != pool.StoragePool || bridge != "" && bridge != pool.Bridge {
			return errOwner
		}
		storagePool, bridge = pool.StoragePool, pool.Bridge
		pool.ZFSDataset = c.Storage.RootDataset + "/configuration-validation/pool"
		spec.Pools[id] = pool
	}
	if _, e = nativeapi.NewPolicy(spec); e != nil {
		return e
	}
	return nil
}

func distinctCapsuleRoles(leaves ...string) bool {
	seen := map[string]bool{}
	for _, leaf := range leaves {
		if !capsuleHash(leaf) || seen[leaf] {
			return false
		}
		seen[leaf] = true
	}
	return len(leaves) == 4
}

func capsuleTrust(c controlConfig) (*tls.Config, *tls.Config, error) {
	server, e := rootPair(c.ServerCertificate, c.ServerKey)
	if e != nil {
		return nil, nil, e
	}
	owner, e := rootPair(c.OwnerCertificate, c.OwnerKey)
	if e != nil {
		return nil, nil, e
	}
	if !distinctCapsuleRoles(nativeidentity.Fingerprint(server.Certificate[0]), nativeidentity.Fingerprint(owner.Certificate[0]), c.ControllerClientLeaf, c.ControllerServerLeaf) {
		return nil, nil, errOwner
	}
	ca, e := rootData(c.ControllerCA, false, 1<<20)
	if e != nil {
		return nil, nil, e
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, nil, errOwner
	}
	return &tls.Config{Certificates: []tls.Certificate{server}, ClientCAs: roots, MinVersion: tls.VersionTLS13}, &tls.Config{Certificates: []tls.Certificate{owner}, RootCAs: roots, MinVersion: tls.VersionTLS13}, nil
}

// serveConfiguredCapsule owns the concrete construction order. There are no
// injected authority callbacks, imported receipts or retry/adoption paths.
// Any failure after birth leaves its permanent journals and resources intact.
// The public command gate and NewExecutionServer boundary gate remain closed.
func serveConfiguredCapsule(ctx context.Context, path string) error {
	if ctx == nil || ctx.Err() != nil {
		return errOwner
	}
	if e := verifyManagerMode(true); e != nil {
		return e
	}
	c, e := loadCapsuleControl(path)
	if e != nil {
		return e
	}
	serverTLS, ownerTLS, e := capsuleTrust(c.Control)
	if e != nil {
		return e
	}
	quota, e := nativequota.New(c.QuotaReader)
	if e != nil {
		return e
	}
	pools, e := nativequota.NewPoolReader(c.Control.PoolReader)
	if e != nil {
		return e
	}
	networkPolicy, e := nativeegress.Compile(c.Network)
	if e != nil {
		return e
	}
	limits, e := nativeboundary.Observe(c.Limits)
	if e != nil {
		return e
	}
	// This is a self-placement transition in the already verified private
	// manager hierarchy, not movement of a sampled child PID. It is one-use.
	if e = nativeboundary.PlaceOwner(limits); e != nil {
		return e
	}
	artifacts, e := nativezfs.CreateHostArtifactRoot(ctx, c.Artifacts, pools)
	if e != nil {
		return e
	}
	// Closing drops retained descriptors only, never unmounts or deletes.
	defer artifacts.Close()
	paths, e := artifacts.Paths(ctx)
	if e != nil {
		return e
	}
	c.Launch.Image, c.Launch.Registry, c.Launch.StateParent = paths.Image, paths.IDs, paths.State
	c.Storage.Registry, c.NetworkTools.Registry, c.DeviceRegistry = paths.ZFS, paths.Egress, paths.Devices
	// Image copying may outlast an observation; refresh the same exact manager.
	freshLimits, e := nativeboundary.Observe(c.Limits)
	if e != nil || freshLimits.NamespaceInode() != limits.NamespaceInode() || freshLimits.RootInode() != limits.RootInode() {
		return errOwner
	}
	limits = freshLimits
	devices, e := nativedevice.Install(ctx, limits, c.DeviceRegistry, c.DevicePinRoot)
	if e != nil {
		return e
	}
	staged, e := nativecapsule.StartWithArtifacts(ctx, c.Launch, limits, devices, c.Partition, artifacts)
	if e != nil {
		return e
	}
	defer staged.Close() // descriptor close only, never kill/release/rollback
	storage, e := nativezfs.CreateFresh(ctx, staged.Peer(), c.Storage)
	if e != nil {
		return e
	}
	network, e := nativeegress.CreateFresh(ctx, staged.Peer(), networkPolicy, c.NetworkTools)
	if e != nil {
		return e
	}
	running, e := staged.Finish(ctx, storage, c.Control.Policy.NativeSHA256, c.RuntimeQuotaHelperSHA256, network)
	if e != nil {
		return e
	}
	spec := c.Control.Policy
	spec.DomainID = running.BirthID()
	spec.EgressDigest = network.Policy().Digest()
	for id, pool := range spec.Pools {
		pool.ZFSDataset = storage.Record().Dataset + "/pool"
		spec.Pools[id] = pool
	}
	policy, e := nativeapi.NewPolicy(spec)
	if e != nil {
		return e
	}
	api, e := nativeapi.NewCapsuleWithEgress(ctx, running, policy, network)
	if e != nil {
		return e
	}
	if e = running.SealStorageBudgets(ctx, c.StorageBudgets); e != nil {
		return e
	}
	claims, e := nativecontrol.NewClaimClient(nativecontrol.ClaimConfiguration{Endpoint: c.Control.ControllerURL, TLS: ownerTLS, ControllerServerLeaf: c.Control.ControllerServerLeaf, OwnerClientLeaf: nativeidentity.Fingerprint(ownerTLS.Certificates[0].Certificate[0]), DomainID: policy.DomainID(), PolicyDigest: policy.Digest()})
	if e != nil {
		return e
	}
	defer claims.Close()
	server, e := nativecontrol.NewExecutionServer(api, policy, claims, quota, pools, serverTLS, ownerTLS, c.Control.ControllerClientLeaf)
	if e != nil {
		return e
	} // Current capsule boundary is deliberately refused.
	return serveCapsuleListener(ctx, c.Control.Listen, server, running)
}

func serveCapsuleListener(ctx context.Context, address string, server *http.Server, running *nativecapsule.Running) error {
	if ctx == nil || server == nil || running == nil || running.CheckScope(ctx) != nil {
		return errOwner
	}
	listener, e := net.Listen("tcp", address)
	if e != nil {
		return errOwner
	}
	defer listener.Close()
	ended := make(chan error, 1)
	serverEnded := false
	go func() { ended <- server.ServeTLS(listener, "", "") }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
		}
		if !serverEnded {
			<-ended
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ended:
			serverEnded = true
			return errOwner
		case <-ticker.C:
			if running.CheckScope(ctx) != nil {
				return errOwner
			}
		}
	}
}
