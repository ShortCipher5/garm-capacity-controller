package incusapi

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestObserverDirectReadsAndRejectsWrites(t *testing.T) {
	var received atomic.Int32
	var spoofRoot atomic.Bool
	var serverCert atomic.Pointer[x509.Certificate]
	server, creds := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.Method != http.MethodGet || r.URL.Query().Get("project") != "default" {
			t.Error("observer escaped its method or project boundary")
			http.Error(w, "denied", 403)
			return
		}
		switch r.URL.Path {
		case "/1.0":
			cert := serverCert.Load()
			if cert == nil {
				t.Error("fixture certificate not initialized")
				http.Error(w, "fixture unavailable", 500)
				return
			}
			hash := fmt.Sprintf("%x", sha256.Sum256(cert.Raw))
			if spoofRoot.Load() {
				hash = strings.Repeat("0", 64)
			}
			writeSync(w, map[string]any{"api_version": "1.0", "auth": "trusted", "environment": map[string]any{
				"server_clustered": false, "server_name": "incus-fixture", "server_version": "7.0.2",
				"certificate": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})), "certificate_fingerprint": hash,
			}})
		case "/1.0/instances":
			writeSync(w, []any{map[string]any{"name": "runner-one", "status_code": 103, "expanded_config": map[string]string{
				"limits.cpu": "2", "limits.memory": "2048MiB", "user.runner-controller-id": "controller-one",
				"user.runner-pool-id": "pool-one", "user.user-data": "FIXTURE-INSTANCE-SENTINEL",
			}}})
		case "/1.0/operations":
			writeSync(w, map[string]any{"running": []any{map[string]any{
				"id": operation, "status_code": 103, "resources": map[string][]string{"instances": {"/1.0/instances/runner-one?project=default"}},
				"metadata": map[string]string{"test": "FIXTURE-OPERATION-SENTINEL"},
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	cert, err := x509.ParseCertificate(server.TLS.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	serverCert.Store(cert)
	pin := fmt.Sprintf("%x", sha256.Sum256(cert.RawSubjectPublicKeyInfo))
	if bad, err := NewObserver(server.URL, "default", "invalid", creds); err == nil || bad != nil {
		t.Fatal("accepted malformed server key pin")
	}
	bad, err := NewObserver(server.URL, "default", strings.Repeat("0", 64), creds)
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	if _, err = bad.Instances(context.Background()); !errors.Is(err, ErrTransport) || received.Load() != 0 {
		t.Fatalf("wrong pin reached HTTP handler or did not fail closed: %v", err)
	}
	client, err := NewObserver(server.URL, "default", pin, creds)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	instances, err := client.Instances(ctx)
	if err != nil || len(instances) != 1 || instances[0].CPUMillis != 2000 {
		t.Fatalf("scoped instance read failed: %v", err)
	}
	ops, err := client.Operations(ctx)
	if err != nil || len(ops) != 1 || len(ops[0].Instances) != 1 || ops[0].Instances[0] != "runner-one" {
		t.Fatalf("scoped operations read failed: %v", err)
	}
	data, err := json.Marshal([]any{instances, ops})
	if err != nil || strings.Contains(string(data), "SENTINEL") {
		t.Fatal("typed inventory exposed unrelated sensitive metadata")
	}
	spoofRoot.Store(true)
	if _, err := client.Instances(ctx); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("mismatch between server TLS pin and Incus identity did not fail closed: %v", err)
	}
	spoofRoot.Store(false)
	before := received.Load()
	for _, req := range []struct {
		method, path string
		query        url.Values
		body         any
	}{
		{http.MethodPost, "/1.0/instances", nil, nil},
		{http.MethodDelete, "/1.0/instances/runner-one", nil, nil},
		{http.MethodGet, "/1.0/instances/runner-one/exec", nil, nil},
		{http.MethodGet, "/1.0/operations/" + operation + "/wait", nil, nil},
		{http.MethodGet, "/1.0/certificates", nil, nil},
		{http.MethodGet, "/1.0/instances", url.Values{"recursion": {"1"}, "project": {"other"}}, nil},
		{http.MethodGet, "/1.0/instances", url.Values{"recursion": {"1", "1"}}, nil},
		{http.MethodGet, "/1.0", nil, struct{}{}},
	} {
		if _, err := client.request(ctx, req.method, req.path, req.query, req.body); !errors.Is(err, ErrUnsupported) {
			t.Errorf("unexpected observer request outcome for %s %s: %v", req.method, req.path, err)
		}
	}
	if received.Load() != before {
		t.Fatal("blocked request reached the network")
	}
}
