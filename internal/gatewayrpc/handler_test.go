package gatewayrpc

import (
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
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
	common "github.com/cloudbase/garm-provider-common/params"
)

func rpcTLSFixture(t *testing.T, handler http.Handler) (*httptest.Server, *tls.Config) {
	t.Helper()
	server, credentials, _ := rpcTLSRolesFixture(t, handler)
	return server, credentials
}
func rpcTLSRolesFixture(t *testing.T, handler http.Handler) (*httptest.Server, *tls.Config, func(int64) *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "disposable-test-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	encoded, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(encoded)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	cert := func(serial int64, client bool) tls.Certificate {
		leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "disposable"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		if client {
			template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		} else {
			template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}
		leaf, err := x509.CreateCertificate(rand.Reader, template, parsed, &leafKey.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{leaf, encoded}, PrivateKey: leafKey}
	}
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert(2, false)}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS13}
	server.StartTLS()
	t.Cleanup(server.Close)
	credentials := func(serial int64) *tls.Config {
		return &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{cert(serial, true)}, MinVersion: tls.VersionTLS13}
	}
	return server, credentials(3), credentials
}

type backend struct {
	identity  *incusapi.Client
	mu        sync.Mutex
	instances map[string]incusapi.Instance
	creates   atomic.Int32
}

func (b *backend) PhysicalIdentity(ctx context.Context) (incusapi.PhysicalServerIdentity, error) {
	return b.identity.PhysicalIdentity(ctx)
}
func (b *backend) PinPhysicalIdentity(value incusapi.PhysicalServerIdentity) error {
	return b.identity.PinPhysicalIdentity(value)
}
func (b *backend) Create(_ context.Context, s incusapi.InstanceSpec) (incusapi.Created, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.creates.Add(1)
	b.instances[s.Name] = incusapi.Instance{Name: s.Name, Status: 103, CPUMillis: s.CPUMillis, RAMMiB: s.RAMMiB, ControllerID: s.ControllerID, PoolID: s.PoolID, ReservationKey: s.ReservationKey}
	return incusapi.Created{OperationID: "12345678-1234-1234-1234-123456789abc", ResourceID: s.Name}, nil
}
func (b *backend) Wait(context.Context, incusapi.Created) error { return nil }
func (b *backend) Instance(_ context.Context, name string) (incusapi.Instance, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.instances[name], nil
}
func (b *backend) Instances(context.Context) ([]incusapi.Instance, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rows := []incusapi.Instance{}
	for _, row := range b.instances {
		rows = append(rows, row)
	}
	return rows, nil
}
func (b *backend) Operations(context.Context) ([]incusapi.Operation, error) {
	return []incusapi.Operation{}, nil
}

type source struct{}

func (source) Instances(context.Context) ([]gateway.GarmInstance, error) {
	return []gateway.GarmInstance{}, nil
}
func facade(t *testing.T) (*gateway.Facade, *backend) {
	t.Helper()
	budget := admission.Resources{CPU: 2000, RAM: 4096, Runners: 2}
	config := admission.Config{Global: budget, Hosts: map[string]admission.Host{"a": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool-a": {Budget: budget, Hosts: []string{"a"}}}, MaxObservationAge: time.Minute}
	store, err := admission.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	b := &backend{instances: map[string]incusapi.Instance{}, identity: testsupport.Identity(t, "rpc-fixture-host").Client}
	f, err := gateway.New(context.Background(), store, "controller-a", map[string]gateway.PoolPolicy{"pool-a": {Host: "a", CPU: 1000, RAM: 1024, Flavor: "small", Image: "linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"base"}, Type: "container", Arch: common.Amd64}}, map[string]gateway.Backend{"a": b})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Reconcile(context.Background(), source{}); err != nil {
		t.Fatal(err)
	}
	return f, b
}
func runningFixture(t *testing.T, provision bool) (*Client, *backend, *Handler, *tls.Config, string) {
	t.Helper()
	f, b := facade(t)
	var handler *Handler
	server, credentials := rpcTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	hash := sha256.Sum256(credentials.Certificates[0].Certificate[0])
	var err error
	handler, err = NewHandler(f, []string{hex.EncodeToString(hash[:])}, provision)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(server.URL, credentials)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client, b, handler, credentials, server.URL
}
func bootstrap() common.BootstrapInstance {
	osType := "linux"
	arch := "x64"
	file := "runner.tar.gz"
	download := "https://example.invalid/runner.tar.gz"
	return common.BootstrapInstance{Name: "runner-1", PoolID: "pool-a", OSType: common.Linux, OSArch: common.Amd64, Flavor: "small", Image: "linux", InstanceToken: "disposable-secret", CallbackURL: "https://example.invalid/callback", MetadataURL: "https://example.invalid/metadata", Tools: []common.RunnerApplicationDownload{{OS: &osType, Architecture: &arch, Filename: &file, DownloadURL: &download}}, ExtraSpecs: json.RawMessage(`{}`)}
}
func TestAuthenticatedWireAdmissionCreateReadAndReplay(t *testing.T) {
	client, b, _, _, _ := runningFixture(t, true)
	request := garmprotocol.Request{Protocol: garmprotocol.Version, Name: "runner-1", PoolID: "pool-a"}
	exit, data, err := client.Admit(context.Background(), request)
	if err != nil || exit != 0 {
		t.Fatalf("admit %s %v", data, err)
	}
	if _, err := garmprotocol.Parse(exit, data, request); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		instance, err := client.Create(context.Background(), bootstrap())
		if err != nil || instance.Name != "runner-1" {
			t.Fatalf("create %+v %v", instance, err)
		}
	}
	if b.creates.Load() != 1 {
		t.Fatal("wire replay created twice")
	}
	instance, err := client.Get(context.Background(), Identity{Name: "runner-1", PoolID: "pool-a"})
	if err != nil || instance.Name != "runner-1" {
		t.Fatal(err)
	}
	rows, err := client.List(context.Background(), "pool-a")
	if err != nil || len(rows) != 1 {
		t.Fatalf("list %+v %v", rows, err)
	}
}
func TestReadOnlyByDefaultAndNoMutationRoutes(t *testing.T) {
	client, b, _, _, _ := runningFixture(t, false)
	if _, _, err := client.Admit(context.Background(), garmprotocol.Request{Protocol: garmprotocol.Version, Name: "runner-1", PoolID: "pool-a"}); err == nil {
		t.Fatal("read-only admission succeeded")
	}
	if _, err := client.Create(context.Background(), bootstrap()); err == nil {
		t.Fatal("read-only create succeeded")
	}
	var result any
	for _, path := range []string{"/v1/delete", "/v1/reconcile", "/v1/config", "/v1/admin", "/v1/host-mode"} {
		if err := client.call(context.Background(), path, Identity{PoolID: "pool-a"}, &result); err == nil {
			t.Fatal("unimplemented route succeeded")
		}
	}
	if b.creates.Load() != 0 {
		t.Fatal("read-only listener mutated")
	}
}
func TestCertificateAllowlistAndNoPlaintextTrust(t *testing.T) {
	_, _, handler, credentials, _ := runningFixture(t, true)
	request := httptest.NewRequest("POST", "https://example.invalid/v1/list", strings.NewReader(`{"pool_id":"pool-a"}`))
	request.Header.Set("Content-Type", "application/json")
	for _, state := range []*tls.ConnectionState{nil, {PeerCertificates: []*x509.Certificate{{Raw: credentials.Certificates[0].Certificate[0]}}}, {VerifiedChains: [][]*x509.Certificate{{{Raw: []byte("other")}}}, PeerCertificates: []*x509.Certificate{{Raw: []byte("other")}}}} {
		request.TLS = state
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized && response.Code != http.StatusForbidden {
			t.Fatal("unverified peer accepted")
		}
	}
}
func TestExactBoundedRequestDecoding(t *testing.T) {
	for _, body := range []string{`null`, `{"Name":"one","pool_id":"pool"}`, `{"name":"one","name":"two","pool_id":"pool"}`, `{"name":"one","pool_id":"pool","extra":true}`, strings.Repeat(" ", MaximumRequestBytes+1)} {
		var request Identity
		if Decode(strings.NewReader(body), &request) == nil {
			t.Fatal("ambiguous request accepted")
		}
	}
	var request common.BootstrapInstance
	valid, _ := json.Marshal(bootstrap())
	if err := Decode(strings.NewReader(string(valid)), &request); err != nil {
		t.Fatalf("canonical Garm nil optional fields rejected %v", err)
	}
	for _, body := range []string{`{"name":"one","tools":[{"OS":"linux"}]}`, `{"name":"one","user_data_options":{"enable_boot_debug":true,"enable_boot_debug":false}}`} {
		if Decode(strings.NewReader(body), &request) == nil {
			t.Fatal("nested ambiguous keys accepted")
		}
	}
}

func TestAllowlistAndConcurrencyGateRejectOverWire(t *testing.T) {
	client, _, handler, _, _ := runningFixture(t, true)
	original := handler.fingerprints
	handler.fingerprints = map[string]bool{}
	if _, err := client.List(context.Background(), "pool-a"); err == nil {
		t.Fatal("CA trusted but unlisted client accepted")
	}
	handler.fingerprints = original
	for i := 0; i < cap(handler.inFlight); i++ {
		handler.inFlight <- struct{}{}
	}
	if _, err := client.List(context.Background(), "pool-a"); err == nil {
		t.Fatal("in-flight bound bypassed")
	}
	for len(handler.inFlight) > 0 {
		<-handler.inFlight
	}
	if _, err := client.Get(context.Background(), Identity{Name: "uncertain", PoolID: "pool-a"}); err == nil {
		t.Fatal("absent/uncertain get reported confirmed")
	}
}

func TestConfigMapsRejectDuplicateAndCaseFoldedKeys(t *testing.T) {
	type policy struct {
		CPU int `json:"cpu"`
	}
	type config struct {
		Pools     map[string]policy `json:"pools"`
		Scalesets map[uint64]string `json:"scalesets"`
	}
	for _, body := range []string{`{"pools":{"one":{"cpu":1,"cpu":2}}}`, `{"pools":{"one":{"CPU":2}}}`, `{"pools":{"one":{"cpu":1},"one":{"cpu":2}}}`, `{"scalesets":{"1":"one","01":"two"}}`} {
		var value config
		if Decode(strings.NewReader(body), &value) == nil {
			t.Fatal("ambiguous configuration map accepted")
		}
	}
	var value config
	if Decode(strings.NewReader(`{"pools":{"one":{"cpu":1}},"scalesets":{"1":"one"}}`), &value) != nil {
		t.Fatal("canonical config rejected")
	}
}

func TestObservedTimeUsesCanonicalJSONRepresentation(t *testing.T) {
	type config struct {
		Hosts map[string]admission.Host `json:"hosts"`
	}
	input := config{Hosts: map[string]admission.Host{"a": {ObservedAt: time.Now().UTC()}}}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var output config
	if err := Decode(strings.NewReader(string(data)), &output); err != nil {
		t.Fatalf("canonical observed_at rejected: %v", err)
	}
	if !input.Hosts["a"].ObservedAt.Equal(output.Hosts["a"].ObservedAt) {
		t.Fatal("observation time changed")
	}
	for _, body := range []string{`{"hosts":{"a":{"observed_at":{}}}}`, `{"hosts":{"a":{"observed_at":"not-a-time"}}}`, `{"hosts":{"a":{"observed_at":null}}}`} {
		if Decode(strings.NewReader(body), &output) == nil {
			t.Fatal("invalid timestamp accepted")
		}
	}
}

func TestAuthenticatedCancellationIsIdempotentAndCannotRecreate(t *testing.T) {
	ctx := context.Background()
	client, b, _, _, _ := runningFixture(t, true)
	req := garmprotocol.Request{Protocol: garmprotocol.Version, Name: "runner-1", PoolID: "pool-a"}
	if exit, _, err := client.Admit(ctx, req); err != nil || exit != 0 {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		receipt, err := client.CancelAdmission(ctx, req)
		if err != nil || receipt.Replay == nil || *receipt.Replay != (i == 1) {
			t.Fatalf("cancel %+v %v", receipt, err)
		}
	}
	if _, err := client.Create(ctx, bootstrap()); err == nil {
		t.Fatal("cancelled create accepted")
	}
	if _, _, err := client.Admit(ctx, req); err == nil {
		t.Fatal("cancelled admit accepted")
	}
	req.Name = "replacement"
	if exit, _, err := client.Admit(ctx, req); err != nil || exit != 0 {
		t.Fatal(err)
	}
	if b.creates.Load() != 0 {
		t.Fatal("cancellation dispatched")
	}
	readonly, _, _, _, _ := runningFixture(t, false)
	if _, err := readonly.CancelAdmission(ctx, req); err == nil {
		t.Fatal("read-only cancellation accepted")
	}
}

func TestCancellationRejectsUnverifiedAndUnlistedPeers(t *testing.T) {
	_, b, handler, credentials, _ := runningFixture(t, true)
	for _, state := range []*tls.ConnectionState{nil, {PeerCertificates: []*x509.Certificate{{Raw: credentials.Certificates[0].Certificate[0]}}}, {VerifiedChains: [][]*x509.Certificate{{{Raw: []byte("other")}}}, PeerCertificates: []*x509.Certificate{{Raw: []byte("other")}}}} {
		r := httptest.NewRequest("POST", "https://example.invalid/v1/cancel-admission", strings.NewReader(`{"protocol":"garm-capacity/v1","name":"runner-1","pool_id":"pool-a"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer read-token-is-not-provider-auth")
		r.TLS = state
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		if response.Code != http.StatusUnauthorized && response.Code != http.StatusForbidden {
			t.Fatalf("wrong role accepted %d", response.Code)
		}
	}
	if b.creates.Load() != 0 {
		t.Fatal("invalid role made external call")
	}
}
