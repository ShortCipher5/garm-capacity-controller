package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	common "github.com/cloudbase/garm-provider-common/params"
)

type testCredentials struct {
	root          *x509.CertPool
	server        tls.Certificate
	provider      tls.Certificate
	other         tls.Certificate
	serverFiles   securefiles.TLSFiles
	providerFiles securefiles.TLSFiles
	otherFiles    securefiles.TLSFiles
}

func incusIdentityRoot(keys testCredentials, name string) map[string]any {
	hash := sha256.Sum256(keys.server.Certificate[0])
	return map[string]any{"api_version": "1.0", "auth": "trusted", "api_extensions": []string{"instance_create_start"}, "environment": map[string]any{"server_clustered": false, "server_name": name, "server_version": "6.0.4", "certificate": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: keys.server.Certificate[0]})), "certificate_fingerprint": fmt.Sprintf("%x", hash)}}
}

func credentials(t *testing.T) testCredentials {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "disposable-lifecycle-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	caPath := write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	pair := func(serial int64, client bool) (tls.Certificate, securefiles.TLSFiles) {
		private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "disposable-leaf"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		if client {
			leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		}
		encoded, err := x509.CreateCertificate(rand.Reader, leaf, parsed, &private.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		privateDER, err := x509.MarshalPKCS8PrivateKey(private)
		if err != nil {
			t.Fatal(err)
		}
		name := big.NewInt(serial).String()
		certPEM := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: encoded}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
		files := securefiles.TLSFiles{CA: caPath, Certificate: write(name+".pem", certPEM), Key: write(name+".key", keyPEM)}
		return tls.Certificate{Certificate: [][]byte{encoded, der}, PrivateKey: private}, files
	}
	server, serverFiles := pair(2, false)
	provider, providerFiles := pair(3, true)
	other, otherFiles := pair(4, true)
	return testCredentials{roots, server, provider, other, serverFiles, providerFiles, otherFiles}
}
func mutualServer(t *testing.T, keys testCredentials, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{keys.server}, ClientCAs: keys.root, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}
func syncJSON(w http.ResponseWriter, metadata any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "sync", "status_code": 200, "metadata": metadata})
}

func TestServiceActualTLSStartupReconcileReadOnlyAndShutdown(t *testing.T) {
	for _, provision := range []bool{false, true} {
		t.Run(map[bool]string{false: "default-read-only", true: "explicit-local-fixture-opt-in"}[provision], func(t *testing.T) {
			keys := credentials(t)
			var garmFailure atomic.Bool
			var creates atomic.Int32
			incus := mutualServer(t, keys, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("project") != "runners" {
					t.Error("wrong physical scope")
				}
				if r.Method != "GET" {
					creates.Add(1)
					w.WriteHeader(503)
					return
				}
				switch r.URL.Path {
				case "/1.0":
					syncJSON(w, incusIdentityRoot(keys, "lifecycle-incus"))
				case "/1.0/instances":
					syncJSON(w, []any{})
				case "/1.0/operations":
					syncJSON(w, map[string]any{})
				default:
					http.NotFound(w, r)
				}
			}))
			garm := mutualServer(t, keys, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || (r.URL.Path != "/api/v1/instances" && r.URL.Path != "/api/v1/pools/pool-a") || r.Header.Get("Authorization") != "Bearer disposable-lifecycle-token" {
					t.Error("wrong Garm route/auth")
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if garmFailure.Load() {
					w.WriteHeader(503)
					_, _ = w.Write([]byte("private-fault-must-not-log"))
					return
				}
				if r.URL.Path == "/api/v1/pools/pool-a" {
					_ = json.NewEncoder(w).Encode(sourcePoolMetadataFixture("pool-a"))
					return
				}
				_, _ = w.Write([]byte(`[]`))
			}))
			directory := t.TempDir()
			token := filepath.Join(directory, "garm.token")
			if err := os.WriteFile(token, []byte("disposable-lifecycle-token"), 0600); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			hash := sha256.Sum256(keys.provider.Certificate[0])
			budget := admission.Resources{CPU: 2000, RAM: 4096, Runners: 2}
			config := configuration{Listen: address, Controller: "controller-test", Database: filepath.Join(directory, "ledger.db"), InitialPolicy: admission.Config{Global: budget, Hosts: map[string]admission.Host{"a": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool-a": {Budget: budget, Hosts: []string{"a"}}}, MaxObservationAge: 3 * time.Second}, Pools: map[string]gateway.PoolPolicy{"pool-a": {Host: "a", CPU: 1000, RAM: 1024, Flavor: "small", Image: "linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"base"}, Type: "container", Arch: common.Amd64}}, Hosts: map[string]hostConfiguration{"a": {Endpoint: incus.URL, Project: "runners", TLS: keys.otherFiles}}, Garm: garmConfiguration{Endpoint: garm.URL, TokenFile: token, TLS: keys.otherFiles, Providers: []string{"capacity"}}, ServerTLS: keys.serverFiles, ProviderFingerprints: []string{hex.EncodeToString(hash[:])}}

			if provision {
				sourceHash := sha256.Sum256(keys.other.Certificate[0])
				sourceFingerprint := hex.EncodeToString(sourceHash[:])
				generation := uint64(0)
				jit := true
				config.SourceFingerprints = []string{sourceFingerprint}
				config.BootstrapMode = "gateway-owned-default-cloudinit/v1"
				config.PreparationPools = map[string]gateway.PreparationPoolPolicy{"pool-a": {ProviderName: "capacity", EntityType: "repository", EntityID: "22222222-2222-4222-8222-222222222222", ForgeEndpointName: "github-fixture", Generation: &generation, JITEnabled: &jit, SourceFingerprints: []string{sourceFingerprint}}}
			}

			adminListener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			adminAddress := adminListener.Addr().String()
			adminToken := strings.Repeat("c", 64)
			adminTokenPath := filepath.Join(directory, "admin.token")
			if err := os.WriteFile(adminTokenPath, []byte(adminToken), 0600); err != nil {
				t.Fatal(err)
			}
			assets := filepath.Join(directory, "ui")
			if err := os.Mkdir(assets, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(assets, "index.html"), []byte("<!doctype html><title>Read-only app shell</title>"), 0600); err != nil {
				t.Fatal(err)
			}
			config.Admin = &adminConfiguration{Listen: adminAddress, TLS: keys.serverFiles, TokenFile: adminTokenPath, Assets: assets}
			// Exercise the real private JSON configuration path, not only typed setup.
			rawConfig, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(directory, "gateway.local.json")
			if err := os.WriteFile(configPath, rawConfig, 0600); err != nil {
				t.Fatal(err)
			}
			privateConfig, err := securefiles.Read(configPath, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			var decoded configuration
			if err := gatewayrpc.Decode(bytes.NewReader(privateConfig), &decoded); err != nil {
				t.Fatalf("canonical full gateway configuration rejected: %v", err)
			}
			config = decoded
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			initializeRuntimeFixture(t, &config)
			go func() { done <- serveOnListeners(ctx, config, provision, listener, adminListener) }()
			defer cancel()
			clientTLS := &tls.Config{RootCAs: keys.root, Certificates: []tls.Certificate{keys.provider}, MinVersion: tls.VersionTLS13}
			client, err := gatewayrpc.NewClient("https://"+address, clientTLS)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			deadline := time.Now().Add(5 * time.Second)
			for {
				check, stop := context.WithTimeout(context.Background(), 200*time.Millisecond)
				_, err = client.List(check, "pool-a")
				stop()
				if err == nil {
					break
				}
				if time.Now().After(deadline) {
					cancel()
					t.Fatalf("actual service startup failed %v", err)
				}
				time.Sleep(10 * time.Millisecond)
			}
			adminClient := &http.Client{Timeout: time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: keys.root, MinVersion: tls.VersionTLS13}}}
			defer adminClient.CloseIdleConnections()
			for _, authorized := range []bool{false, true} {
				request, _ := http.NewRequest("GET", "https://"+adminAddress+"/admin/api/v1/overview", nil)
				if authorized {
					request.Header.Set("Authorization", "Bearer "+adminToken)
				}
				response, err := adminClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				if !authorized {
					response.Body.Close()
					if response.StatusCode != 401 {
						t.Fatal("admin view lacked independent bearer authentication")
					}
					continue
				}
				var view adminview.Overview
				err = json.NewDecoder(response.Body).Decode(&view)
				response.Body.Close()
				if response.StatusCode != 200 || err != nil || view.Provisioning != provision || view.Mode != "experimental-ledger" {
					t.Fatalf("actual admin view %+v %v status%d", view, err, response.StatusCode)
				}
			}
			mutation, _ := http.NewRequest("POST", "https://"+adminAddress+"/v1/create", strings.NewReader(`{}`))
			mutation.Header.Set("Authorization", "Bearer "+adminToken)
			response, err := adminClient.Do(mutation)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode < 400 {
				t.Fatal("read-only admin token reached create route")
			}
			request := garmprotocol.Request{Protocol: garmprotocol.Version, Name: "runner-1", PoolID: "pool-a"}
			exit, _, err := client.Admit(context.Background(), request)
			if provision {
				if err != nil || exit != 0 {
					t.Fatalf("explicit opt-in admission %d %v", exit, err)
				}
			} else if err == nil {
				t.Fatal("default server permitted reserve")
			}
			// A different CA-trusted leaf is not authorized as the Garm provider client.
			other, err := gatewayrpc.NewClient("https://"+address, &tls.Config{RootCAs: keys.root, Certificates: []tls.Certificate{keys.other}, MinVersion: tls.VersionTLS13})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := other.List(context.Background(), "pool-a"); err == nil {
				t.Fatal("unlisted trusted leaf accepted")
			}
			other.Close()
			// A trusted server CA alone does not authenticate the client at all.
			noCertificate := &http.Client{Timeout: time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: keys.root, MinVersion: tls.VersionTLS13}}}
			if response, err := noCertificate.Get("https://" + address + "/v1/list"); err == nil {
				response.Body.Close()
				t.Fatal("client certificate not required")
			}
			noCertificate.CloseIdleConnections()
			if provision {
				garmFailure.Store(true)
				state, err := admission.OpenSQLite(config.Database, admission.Config{})
				if err != nil {
					t.Fatal(err)
				}
				deadline = time.Now().Add(5 * time.Second)
				for {
					snapshot, err := state.Snapshot(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					if !snapshot.Ready && snapshot.Epoch > 1 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("failed authoritative reconciliation left readiness open")
					}
					time.Sleep(20 * time.Millisecond)
				}
				state.Close()
				request.Name = "runner-2"
				exit, _, err = client.Admit(context.Background(), request)
				if err != nil || exit != 75 {
					t.Fatalf("failed inventory did not defer %d %v", exit, err)
				}
			}
			if creates.Load() != 0 {
				t.Fatal("admission/read paths dispatched a physical create")
			}
			if !provision {
				// A verified TLS connection that has not finished its first HTTP header is
				// StateNew. net/http deliberately grants it a >5s quiescence window during
				// graceful Shutdown; do not impose a shorter test deadline than the server.
				pending, err := tls.Dial("tcp", address, clientTLS)
				if err != nil {
					t.Fatal(err)
				}
				defer pending.Close()
				if _, err := pending.Write([]byte("POST /v1/list HTTP/1.1\r\nHost: localhost\r\n")); err != nil {
					t.Fatal(err)
				}
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("shutdown %v", err)
				}
			// Both listeners each have a configured 15s graceful window. This
			// bound includes those windows and scheduling margin, not an infinite wait.
			case <-time.After(35 * time.Second):
				t.Fatal("service did not stop")
			}
			adminConnection, adminErr := net.DialTimeout("tcp", adminAddress, 100*time.Millisecond)
			if adminErr == nil {
				adminConnection.Close()
				t.Fatal("admin listener remained open after shutdown")
			}
			connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
			if err == nil {
				connection.Close()
				t.Fatal("listener remained open after shutdown")
			}
		})
	}
}

// An actual Garm response shape, not a caller-supplied source eligibility flag.
func sourcePoolMetadataFixture(id string) map[string]any {
	return map[string]any{"id": id, "provider_name": "capacity", "generation": 0, "repo_id": "22222222-2222-4222-8222-222222222222", "endpoint": map[string]any{"name": "github-fixture"}, "image": "linux", "flavor": "small", "os_type": "linux", "os_arch": "amd64", "enable_shell": false, "enabled": true}
}
