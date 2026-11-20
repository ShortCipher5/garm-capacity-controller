package main

import (
	"encoding/hex"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
	"github.com/ShortCipher5/garm-capacity-controller/internal/remotedisktelemetry"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

// An entry describes an already reviewed read route. None of these values
// come from the remote response, a browser request, or discovered defaults.
type remoteRunnerDiskConfiguration struct {
	Routes []remoteRunnerDiskRouteConfiguration `json:"routes"`
}

type remoteRunnerDiskRouteConfiguration struct {
	HostID          string                `json:"host_id"`
	Origin          string                `json:"origin"`
	ServerKeySHA256 string                `json:"server_key_sha256"`
	ClientKeySHA256 string                `json:"client_key_sha256"`
	ReviewExpiresAt time.Time             `json:"review_expires_at"`
	DirectoryInode  uint64                `json:"directory_inode"`
	Scope           remoteRunnerDiskScope `json:"scope"`
	TLS             securefiles.TLSFiles  `json:"tls"`
}

type remoteRunnerDiskScope struct {
	HostFingerprint string `json:"host_fingerprint"`
	StoragePool     string `json:"storage_pool"`
	BootID          string `json:"boot_id"`
	MountNamespace  uint64 `json:"mount_namespace"`
	MountID         uint64 `json:"mount_id"`
	DeviceMajor     uint32 `json:"device_major"`
	DeviceMinor     uint32 `json:"device_minor"`
}

var errRemoteRunnerDiskConfig = errors.New("read-only remote runner disk configuration unavailable")

// Shape-only: requires a separate admin listener before even the selected
// private remote trust files are opened. It never opens those files itself.
func validateRemoteRunnerDiskConfiguration(c configuration) error {
	if c.EnableRemoteRunnerDisk || c.RemoteRunnerDisk != nil {
		if c.Admin == nil {
			return errRemoteRunnerDiskConfig
		}
		host, _, err := net.SplitHostPort(c.Admin.Listen)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !ip.IsLoopback() {
			return errRemoteRunnerDiskConfig
		}
	}
	_, err := preflightRemoteRunnerDiskRoutes(c.EnableRemoteRunnerDisk, c.RemoteRunnerDisk, c.Hosts, time.Now().UTC())
	return err
}

func remoteRunnerDiskPin(text string) ([32]byte, bool) {
	var pin [32]byte
	if len(text) != 64 || strings.ToLower(text) != text {
		return pin, false
	}
	n, err := hex.Decode(pin[:], []byte(text))
	return pin, err == nil && n == len(pin) && pin != ([32]byte{})
}

func remoteRunnerDiskTrustPaths(files securefiles.TLSFiles) bool {
	if files.CA == files.Certificate || files.CA == files.Key || files.Certificate == files.Key {
		return false
	}
	for _, p := range []string{files.CA, files.Certificate, files.Key} {
		if len(p) < 2 || len(p) > 4096 || !filepath.IsAbs(p) || filepath.Clean(p) != p ||
			!utf8.ValidString(p) || strings.ContainsFunc(p, unicode.IsControl) {
			return false
		}
	}
	return true
}

type reviewedRemoteRunnerDiskRoute struct {
	config    remoteRunnerDiskRouteConfiguration
	binding   adminview.RunnerDiskHealthBinding
	review    remotedisktelemetry.Review
	clientPin [32]byte
}

// preflightRemoteRunnerDiskRoutes is shape-only and performs no I/O. Validate
// every selected entry before reading any explicitly configured local inputs.
func preflightRemoteRunnerDiskRoutes(allowed bool, cfg *remoteRunnerDiskConfiguration, hosts map[string]hostConfiguration, now time.Time) ([]reviewedRemoteRunnerDiskRoute, error) {
	if !allowed && cfg == nil {
		return nil, nil
	}
	if !allowed || cfg == nil || len(cfg.Routes) == 0 || len(cfg.Routes) > 8 {
		return nil, errRemoteRunnerDiskConfig
	}
	routes := make([]reviewedRemoteRunnerDiskRoute, 0, len(cfg.Routes))
	scopes := map[hostinventory.DirFilesystemScope]bool{}
	slots := map[[2]string]bool{}
	origins := map[string]bool{}
	for _, x := range cfg.Routes {
		scope := hostinventory.DirFilesystemScope(x.Scope)
		expiry := x.ReviewExpiresAt.UTC()
		host := x.HostID
		_, known := hosts[host]
		slot := [2]string{host, scope.StoragePool}
		peer, peerOK := remoteRunnerDiskPin(x.ServerKeySHA256)
		client, clientOK := remoteRunnerDiskPin(x.ClientKeySHA256)
		origin, originOK := remotedisktelemetry.HTTPOriginIdentity(x.Origin)
		if !known || len(host) == 0 || len(host) > 256 || !utf8.ValidString(host) || strings.ContainsFunc(host, unicode.IsControl) ||
			!scope.Valid() || x.DirectoryInode == 0 || expiry.Year() < 1 || expiry.Year() > 9999 || !now.UTC().Before(expiry) ||
			!peerOK || !clientOK || peer == client || !originOK || !remoteRunnerDiskTrustPaths(x.TLS) ||
			scopes[scope] || slots[slot] || origins[origin] {
			return nil, errRemoteRunnerDiskConfig
		}
		b := adminview.RunnerDiskHealthBinding{HostID: host, Scope: scope, DirectoryInode: x.DirectoryInode, ReviewExpiresAt: expiry}
		r := remotedisktelemetry.Review{Scope: scope, DirectoryInode: x.DirectoryInode, ExpiresAt: expiry, PeerKeySHA256: peer}
		routes = append(routes, reviewedRemoteRunnerDiskRoute{x, b, r, client})
		scopes[scope], slots[slot], origins[origin] = true, true, true
	}
	return routes, nil
}

// buildRemoteRunnerDiskCoordinator constructs a dormant optional display
// source. A trusted gateway lifetime must separately attach and Run it. No
// endpoint discovery, remote access, fallback trust, enrollment or scaling
// authority is performed or implied by this constructor.
func buildRemoteRunnerDiskCoordinator(allowed bool, cfg *remoteRunnerDiskConfiguration, hosts map[string]hostConfiguration) (*adminview.RemoteRunnerDiskHealthCoordinator, error) {
	routes, err := preflightRemoteRunnerDiskRoutes(allowed, cfg, hosts, time.Now().UTC())
	if err != nil || len(routes) == 0 {
		return nil, err
	}
	in := adminview.RemoteRunnerDiskHealthInput{Enabled: true, Routes: make([]adminview.RemoteRunnerDiskHealthRoute, 0, len(routes))}
	for _, x := range routes {
		trust, err := securefiles.LoadTLS(x.config.TLS)
		if err != nil {
			return nil, errRemoteRunnerDiskConfig
		}
		reader, err := remotedisktelemetry.NewHTTPReader(x.config.Origin, x.review, remotedisktelemetry.HTTPClientTrust{TLS: trust, ClientKeySHA256: x.clientPin})
		if err != nil {
			return nil, errRemoteRunnerDiskConfig
		}
		in.Routes = append(in.Routes, adminview.RemoteRunnerDiskHealthRoute{Binding: x.binding, Review: x.review, Reader: reader})
	}
	coordinator, err := adminview.NewRemoteRunnerDiskHealthCoordinator(in)
	if err != nil {
		return nil, errRemoteRunnerDiskConfig
	}
	return coordinator, nil
}
