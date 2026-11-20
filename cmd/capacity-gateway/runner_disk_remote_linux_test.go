package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"github.com/ShortCipher5/garm-capacity-controller/internal/remotedisktelemetry"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

func remoteRunnerDiskConfigurationFixture(t *testing.T) (remoteRunnerDiskConfiguration, map[string]hostConfiguration, *atomic.Int32) {
	return remoteRunnerDiskConfigurationFixtureHandler(t, nil)
}

func remoteRunnerDiskConfigurationFixtureHandler(t *testing.T, serve http.HandlerFunc) (remoteRunnerDiskConfiguration, map[string]hostConfiguration, *atomic.Int32) {
	t.Helper()
	calls := new(atomic.Int32)
	server, client, _ := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if serve != nil {
			serve(w, r)
		}
	}))
	leaf, err := x509.ParseCertificate(client.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(client.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(der)
	dir := t.TempDir()
	files := securefiles.TLSFiles{
		CA: filepath.Join(dir, "ca.pem"), Certificate: filepath.Join(dir, "client.pem"), Key: filepath.Join(dir, "client.key"),
	}
	write := func(path, typ string, raw []byte) {
		t.Helper()
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: raw}), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(files.CA, "CERTIFICATE", client.Certificates[0].Certificate[1])
	write(files.Certificate, "CERTIFICATE", leaf.Raw)
	write(files.Key, "PRIVATE KEY", der)
	peer := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
	pin := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	x := remoteRunnerDiskRouteConfiguration{
		HostID: "fixture-host", Origin: server.URL, ServerKeySHA256: hex.EncodeToString(peer[:]), ClientKeySHA256: hex.EncodeToString(pin[:]),
		ReviewExpiresAt: time.Now().UTC().Add(time.Minute), DirectoryInode: 9007199254740993,
		Scope: remoteRunnerDiskScope{HostFingerprint: strings.Repeat("a", 64), StoragePool: "fixture-pool", BootID: "11111111-1111-4111-8111-111111111111", MountNamespace: 10, MountID: 15, DeviceMajor: 8, DeviceMinor: 1},
		TLS:   files,
	}
	return remoteRunnerDiskConfiguration{Routes: []remoteRunnerDiskRouteConfiguration{x}}, map[string]hostConfiguration{x.HostID: {}}, calls
}

func serveRemoteRunnerDiskFixture(t *testing.T, x remoteRunnerDiskRouteConfiguration, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1024))
	challenge, valid := remotedisktelemetry.DecodeHTTPChallenge(raw)
	if err != nil || !valid || r.Method != http.MethodPost || r.URL.Path != remotedisktelemetry.HTTPPath ||
		r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
		t.Error("unexpected or unverified remote disk read")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s := x.Scope
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"kind":"runner-dir-disk-display/v1","challenge":"%x","scope":{"host":"%s","pool":"%s","boot":"%s","mount_namespace":%d,"unique_mount_id":%d,"device_major":%d,"device_minor":%d},"directory_inode":%d,"review_expires_at":"%s","observed_at":"%s","total_bytes":8192,"available_bytes":0,"total_inodes":15,"available_inodes":0}`,
		challenge, s.HostFingerprint, s.StoragePool, s.BootID, s.MountNamespace, s.MountID, s.DeviceMajor, s.DeviceMinor,
		x.DirectoryInode, x.ReviewExpiresAt.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
}

func TestRemoteDiskStartupGatesShapeWithoutTrustFileReads(t *testing.T) {
	cfg, hosts, calls := remoteRunnerDiskConfigurationFixture(t)
	c := configuration{RemoteRunnerDisk: &cfg, EnableRemoteRunnerDisk: true, Hosts: hosts, Admin: &adminConfiguration{Listen: "127.0.0.1:0"}}
	cfg.Routes[0].TLS.Key += "-missing"
	if err := validateRemoteRunnerDiskConfiguration(c); err != nil {
		t.Fatalf("valid shape depended on the missing local key: %v", err)
	}
	if _, err := buildRemoteRunnerDiskCoordinator(c.EnableRemoteRunnerDisk, c.RemoteRunnerDisk, c.Hosts); !errors.Is(err, errRemoteRunnerDiskConfig) {
		t.Fatalf("real build ignored missing local key: %v", err)
	}
	c.Admin.Listen = "0.0.0.0:6010"
	if err := validateRemoteRunnerDiskConfiguration(c); !errors.Is(err, errRemoteRunnerDiskConfig) {
		t.Fatalf("nonloopback listener accepted: %v", err)
	}
	c.Admin = nil
	if err := validateRemoteRunnerDiskConfiguration(c); !errors.Is(err, errRemoteRunnerDiskConfig) {
		t.Fatalf("missing separate admin accepted: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("static checks contacted a peer")
	}
}

func TestRemoteRunnerDiskStartupConfigConstructsDormantReaderFromPrivateFiles(t *testing.T) {
	cfg, hosts, calls := remoteRunnerDiskConfigurationFixture(t)
	route := cfg.Routes[0]
	for _, item := range []struct {
		on  bool
		cfg *remoteRunnerDiskConfiguration
	}{
		{false, &cfg}, {true, nil}, {true, &remoteRunnerDiskConfiguration{}},
	} {
		if r, err := buildRemoteRunnerDiskCoordinator(item.on, item.cfg, hosts); r != nil || !errors.Is(err, errRemoteRunnerDiskConfig) {
			t.Fatal("accepted one-sided or empty opt-in", err)
		}
	}
	if r, err := buildRemoteRunnerDiskCoordinator(false, nil, nil); r != nil || err != nil {
		t.Fatal("startup not default-off", err)
	}
	r, err := buildRemoteRunnerDiskCoordinator(true, &cfg, hosts)
	if err != nil {
		t.Fatal("reviewed private role rejected", err)
	}
	if calls.Load() != 0 || len(r.SnapshotRunnerDiskHealth()) != 0 {
		t.Fatal("startup contacted peer or manufactured readings")
	}
	r.Close()
	for _, change := range []func(*remoteRunnerDiskRouteConfiguration){
		func(x *remoteRunnerDiskRouteConfiguration) { x.TLS.Key = x.TLS.Key + "-missing" },
		func(x *remoteRunnerDiskRouteConfiguration) { x.ClientKeySHA256 = strings.Repeat("2", 64) },
	} {
		cfg.Routes[0] = route
		change(&cfg.Routes[0])
		if r, err := buildRemoteRunnerDiskCoordinator(true, &cfg, hosts); r != nil || !errors.Is(err, errRemoteRunnerDiskConfig) {
			t.Fatal("accepted invalid private client role", err)
		}
	}
	cfg.Routes[0] = route
	if err := os.Chmod(route.TLS.Key, 0644); err != nil {
		t.Fatal(err)
	}
	if r, err := buildRemoteRunnerDiskCoordinator(true, &cfg, hosts); r != nil || !errors.Is(err, errRemoteRunnerDiskConfig) {
		t.Fatal("accepted public key file", err)
	}
	if calls.Load() != 0 {
		t.Fatal("failure reached peer", calls.Load())
	}
}

func TestRemoteRunnerDiskStrictStartupShapeAndExactHostScope(t *testing.T) {
	cfg, hosts, calls := remoteRunnerDiskConfigurationFixture(t)
	base := cfg.Routes[0]
	now := time.Now().UTC()
	for given, change := range map[string]func(*remoteRunnerDiskRouteConfiguration){
		"unknown host":           func(x *remoteRunnerDiskRouteConfiguration) { x.HostID = "other" },
		"expiry":                 func(x *remoteRunnerDiskRouteConfiguration) { x.ReviewExpiresAt = now },
		"no inode":               func(x *remoteRunnerDiskRouteConfiguration) { x.DirectoryInode = 0 },
		"missing physical scope": func(x *remoteRunnerDiskRouteConfiguration) { x.Scope.MountNamespace = 0 },
		"same role key":          func(x *remoteRunnerDiskRouteConfiguration) { x.ClientKeySHA256 = x.ServerKeySHA256 },
		"zero pin":               func(x *remoteRunnerDiskRouteConfiguration) { x.ClientKeySHA256 = strings.Repeat("0", 64) },
		"HTTP":                   func(x *remoteRunnerDiskRouteConfiguration) { x.Origin = "http://fixture.invalid" },
		"custom path":            func(x *remoteRunnerDiskRouteConfiguration) { x.Origin = "https://fixture.invalid/custom" },
		"alias hostname":         func(x *remoteRunnerDiskRouteConfiguration) { x.Origin = "https://Fixture.invalid" },
		"relative trust":         func(x *remoteRunnerDiskRouteConfiguration) { x.TLS.Key = "relative.pem" },
	} {
		t.Run(given, func(t *testing.T) {
			x := base
			change(&x)
			if got, err := preflightRemoteRunnerDiskRoutes(true, &remoteRunnerDiskConfiguration{Routes: []remoteRunnerDiskRouteConfiguration{x}}, hosts, now); got != nil || !errors.Is(err, errRemoteRunnerDiskConfig) {
				t.Fatal("invalid startup shape accepted")
			}
		})
	}
	x, y := base, base
	x.Origin, y.Origin = "https://fixture.invalid", "https://fixture.invalid:443/"
	y.Scope.StoragePool = "another"
	y.DirectoryInode++
	if got, err := preflightRemoteRunnerDiskRoutes(true, &remoteRunnerDiskConfiguration{Routes: []remoteRunnerDiskRouteConfiguration{x, y}}, hosts, now); got != nil || !errors.Is(err, errRemoteRunnerDiskConfig) {
		t.Fatal("same fixed origin reused for distinct routes")
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded remoteRunnerDiskConfiguration
	if err := gatewayrpc.Decode(bytes.NewReader(data), &decoded); err != nil || len(decoded.Routes) != 1 || decoded.Routes[0].DirectoryInode != base.DirectoryInode {
		t.Fatal("gateway strict decode lost exact identity", err)
	}
	duplicate := bytes.Replace(data, []byte(`"directory_inode":`), []byte(`"directory_inode":33,"directory_inode":`), 1)
	if err := gatewayrpc.Decode(bytes.NewReader(duplicate), &decoded); err == nil {
		t.Fatal("duplicate independent inode accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("config check contacted peer")
	}
}
