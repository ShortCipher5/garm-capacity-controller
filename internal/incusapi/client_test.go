package incusapi

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const operation = "12345678-1234-1234-1234-123456789abc"

func tlsFixture(t *testing.T, handler http.Handler) (*httptest.Server, *tls.Config) {
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
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert(2, false)}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{cert(3, true)}, MinVersion: tls.VersionTLS12}
}

func writeSync(w http.ResponseWriter, metadata any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "sync", "status_code": 200, "metadata": metadata})
}
func rootMetadata() any {
	return map[string]any{"api_version": "1.0", "auth": "trusted", "api_extensions": []string{"instance_create_start"}}
}
func validSpec() InstanceSpec {
	return InstanceSpec{Name: "runner-1", Type: "container", Profiles: []string{"runner-base"}, ImageFingerprint: strings.Repeat("a", 64), CPUMillis: 2000, RAMMiB: 2048, UserData: "#cloud-config\nsecret-test-bootstrap", ControllerID: "controller-1", PoolID: "pool-1", ReservationKey: strings.Repeat("b", 64)}
}

func TestMutualTLSCreateExplicitLimitsAndWait(t *testing.T) {
	var creates atomic.Int32
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			t.Error("client certificate not verified")
		}
		if r.URL.Query().Get("project") != "runners" {
			t.Error("wrong project scope")
		}
		switch r.URL.Path {
		case "/1.0":
			writeSync(w, rootMetadata())
		case "/1.0/instances":
			if r.Method != "POST" {
				t.Errorf("unexpected method %s", r.Method)
				return
			}
			creates.Add(1)
			var body struct {
				Name     string            `json:"name"`
				Type     string            `json:"type"`
				Start    bool              `json:"start"`
				Config   map[string]string `json:"config"`
				Source   map[string]string `json:"source"`
				Profiles []string          `json:"profiles"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("invalid request")
			}
			if body.Name != "runner-1" || body.Type != "container" || !body.Start || body.Config["limits.cpu"] != "2" || body.Config["limits.memory"] != "2048MiB" || body.Config["limits.memory.enforce"] != "hard" || body.Config["user.garm-capacity.reservation"] != strings.Repeat("b", 64) || body.Source["fingerprint"] != strings.Repeat("a", 64) || body.Source["server"] != "" {
				t.Errorf("unsafe bounded create: %+v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "async", "status_code": 100, "operation": "/1.0/operations/" + operation + "?project=runners", "metadata": map[string]any{"id": operation}})
		case "/1.0/operations/" + operation + "/wait":
			if r.URL.Query().Get("timeout") != "10" {
				t.Error("missing bounded server wait")
			}
			writeSync(w, map[string]any{"id": operation, "status_code": 200, "resources": map[string][]string{"instances": {"/1.0/instances/runner-1?project=runners"}}})
		case "/1.0/instances/runner-1/state":
			writeSync(w, map[string]any{"status_code": 103})
		default:
			t.Errorf("unexpected route: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	client, err := New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	created, err := client.CreateAndWait(context.Background(), validSpec())
	if err != nil || created.OperationID != operation || created.ResourceID != "runner-1" || creates.Load() != 1 {
		t.Fatalf("result: %+v %v creates=%d", created, err, creates.Load())
	}
}

func TestRefusesUnverifiedTLSAndUnsafeEndpoint(t *testing.T) {
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeSync(w, rootMetadata()) }))
	for _, endpoint := range []string{"http://127.0.0.1", "https://user:password@example.invalid", "https://example.invalid/api", "https://example.invalid?token=secret"} {
		if _, err := New(endpoint, "runners", credentials); !errors.Is(err, ErrConfiguration) {
			t.Fatal("unsafe endpoint accepted")
		}
	}
	for _, mode := range []string{"skip", "no-ca", "no-cert", "wrong-name"} {
		broken := credentials.Clone()
		switch mode {
		case "skip":
			broken.InsecureSkipVerify = true
		case "no-ca":
			broken.RootCAs = nil
		case "no-cert":
			broken.Certificates = nil
		case "wrong-name":
			broken.ServerName = "wrong.invalid"
		}
		if _, err := New(server.URL, "runners", broken); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("unsafe TLS %s accepted", mode)
		}
	}
	wrong := credentials.Clone()
	wrong.RootCAs = x509.NewCertPool()
	client, err := New(server.URL, "runners", wrong)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Create(context.Background(), validSpec()); !errors.Is(err, ErrTransport) {
		t.Fatalf("untrusted server accepted: %v", err)
	}
}

func TestCapabilityDenialAndNoAutomaticCreateRetry(t *testing.T) {
	for _, mode := range []string{"missing-extension", "untrusted", "post-error", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			var creates atomic.Int32
			server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/1.0" {
					if mode == "redirect" {
						http.Redirect(w, r, "https://other.invalid/", http.StatusTemporaryRedirect)
						return
					}
					root := map[string]any{"api_version": "1.0", "auth": "trusted", "api_extensions": []string{"instance_create_start"}}
					if mode == "missing-extension" {
						root["api_extensions"] = []string{}
					}
					if mode == "untrusted" {
						root["auth"] = "untrusted"
					}
					writeSync(w, root)
					return
				}
				creates.Add(1)
				http.Error(w, "bootstrap-secret-must-not-echo", http.StatusInternalServerError)
			}))
			client, err := New(server.URL, "runners", credentials)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			_, err = client.Create(context.Background(), validSpec())
			if err == nil || strings.Contains(err.Error(), "bootstrap-secret") {
				t.Fatalf("unsafe error: %v", err)
			}
			want := int32(0)
			if mode == "post-error" {
				want = 1
			}
			if creates.Load() != want {
				t.Fatalf("creates=%d want %d", creates.Load(), want)
			}
		})
	}
}

func TestKnownOperationRetainedOnFailedWait(t *testing.T) {
	var creates atomic.Int32
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/1.0":
			writeSync(w, rootMetadata())
		case "/1.0/instances":
			creates.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "async", "status_code": 100, "operation": "/1.0/operations/" + operation, "metadata": map[string]any{"id": operation}})
		default:
			http.Error(w, "sensitive-user-data", http.StatusServiceUnavailable)
		}
	}))
	client, err := New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	created, err := client.CreateAndWait(context.Background(), validSpec())
	if err == nil || created.OperationID != operation || creates.Load() != 1 || strings.Contains(err.Error(), "sensitive-user-data") {
		t.Fatalf("correlation or redaction failed: %+v %v", created, err)
	}
}

func TestRejectsUnknownResourceLimitsAndConservativelyParsesAllocations(t *testing.T) {
	for input, want := range map[string]int64{"1": 1000, "4": 4000, "0-3,2-4": 5000, "2-2": 1000} {
		got, err := parseCPU(input)
		if err != nil || got != want {
			t.Fatalf("CPU %s: %d %v", input, got, err)
		}
	}
	for _, input := range []string{"", "0", "-1", "0-9999999", "50%", "1.5"} {
		if _, err := parseCPU(input); !errors.Is(err, ErrUnbounded) {
			t.Fatalf("unbounded CPU accepted: %q", input)
		}
	}
	for input, want := range map[string]int64{"1GiB": 1024, "1.5GiB": 1536, "1GB": 954, "1": 1, "2048MiB": 2048} {
		got, err := parseMemory(input)
		if err != nil || got != want {
			t.Fatalf("memory %s: %d %v", input, got, err)
		}
	}
	for _, input := range []string{"", "0MiB", "50%", "-1GiB", "9999999999999999999999999999999999999999999999999999999999999TiB"} {
		if _, err := parseMemory(input); !errors.Is(err, ErrUnbounded) {
			t.Fatalf("unbounded memory accepted: %q", input)
		}
	}
}

func TestInventoryIncludesOrphansButOmitsUserData(t *testing.T) {
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/1.0" {
			writeSync(w, rootMetadata())
			return
		}
		if r.URL.Query().Get("recursion") != "1" {
			t.Error("incomplete inventory query")
		}
		if r.URL.Path == "/1.0/instances" {
			writeSync(w, []any{map[string]any{"name": "orphan", "status_code": 103, "expanded_config": map[string]string{"limits.cpu": "0-1", "limits.memory": "1.5GiB", "user.user-data": "bootstrap-secret"}}})
			return
		}
		writeSync(w, map[string]any{"running": []any{map[string]any{"id": operation, "status_code": 103, "resources": map[string][]string{"instances": {"/1.0/instances/orphan?project=runners"}}}}})
	}))
	client, err := New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	instances, err := client.Instances(context.Background())
	if err != nil || len(instances) != 1 || instances[0].CPUMillis != 2000 || instances[0].RAMMiB != 1536 || instances[0].ControllerID != "" {
		t.Fatalf("orphan inventory: %+v %v", instances, err)
	}
	encoded, _ := json.Marshal(instances)
	if strings.Contains(string(encoded), "bootstrap-secret") {
		t.Fatal("user-data escaped minimal inventory")
	}
	operations, err := client.Operations(context.Background())
	if err != nil || len(operations) != 1 || len(operations[0].Instances) != 1 || operations[0].Instances[0] != "orphan" {
		t.Fatalf("operations: %+v %v", operations, err)
	}
}

func TestMalformedScopeAndNullInventoryFailClosed(t *testing.T) {
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/1.0" {
			writeSync(w, rootMetadata())
			return
		}
		writeSync(w, nil)
	}))
	client, err := New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Instances(context.Background()); !errors.Is(err, ErrResponse) {
		t.Fatal("null inventory treated as empty complete view")
	}
	for _, path := range []string{"https://other.invalid/1.0/operations/" + operation, "/1.0/operations/" + operation + "?project=other", "/1.0/operations/" + operation + "?token=secret", "/1.0/operations/../instances/foo"} {
		if _, err := client.operationPath(path); err == nil {
			t.Fatalf("unsafe operation scope accepted: %s", path)
		}
	}
	if _, err := object([]byte(`{"status_code":400,"status_code":200}`)); err == nil {
		t.Fatal("duplicate status accepted")
	}
}

func TestCreateRejectsResourceOrImageDriftBeforeAnyRequest(t *testing.T) {
	var requests atomic.Int32
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); writeSync(w, rootMetadata()) }))
	client, err := New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, change := range []func(*InstanceSpec){func(s *InstanceSpec) { s.CPUMillis = 1500 }, func(s *InstanceSpec) { s.RAMMiB = 0 }, func(s *InstanceSpec) { s.ImageFingerprint = "https://image.invalid" }, func(s *InstanceSpec) { s.Profiles = nil }, func(s *InstanceSpec) { s.Name = "../other" }, func(s *InstanceSpec) { s.ReservationKey = "raw-secret" }} {
		spec := validSpec()
		change(&spec)
		if _, err := client.Create(context.Background(), spec); !errors.Is(err, ErrConfiguration) {
			t.Fatal("invalid create spec accepted")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid spec reached network")
	}
}

func TestReadsRequireTrustedIdentityAndExactInstance(t *testing.T) {
	for _, mode := range []string{"untrusted", "wrong-name", "duplicates", "null-operations"} {
		t.Run(mode, func(t *testing.T) {
			server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/1.0" {
					if mode == "untrusted" {
						writeSync(w, map[string]any{"api_version": "1.0", "auth": "untrusted"})
						return
					}
					writeSync(w, rootMetadata())
					return
				}
				if mode == "wrong-name" {
					writeSync(w, map[string]any{"name": "other", "status_code": 103, "expanded_config": map[string]string{"limits.cpu": "1", "limits.memory": "1GiB"}})
					return
				}
				if mode == "null-operations" {
					writeSync(w, map[string]any{"running": nil})
					return
				}
				row := map[string]any{"id": operation, "status_code": 103, "resources": map[string][]string{}}
				writeSync(w, map[string]any{"running": []any{row}, "pending": []any{row}})
			}))
			client, err := New(server.URL, "runners", credentials)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if mode == "wrong-name" {
				if _, err := client.Instance(context.Background(), "runner-1"); err == nil {
					t.Fatal("mismatched name accepted")
				}
				return
			}
			if _, err := client.Operations(context.Background()); err == nil {
				t.Fatal("unsafe operation inventory accepted")
			}
		})
	}
}
