package managedgarm

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStockPilotBootstrapProvisionalReview(t *testing.T) {
	id, complete := stockAuthFixture(t)
	var user credential
	if !stockBindingJSON(complete[4], &user) {
		t.Fatal("fixture")
	}
	review := stockPilotBootstrapReview{"stock-pilot-bootstrap/v1", id, stockBindingDigest(complete[1]), credentialHash(user)}
	reviewed, _ := json.Marshal(review)
	raw := [3][]byte{reviewed, complete[1], complete[4]}
	got, trust, err := validateStockPilotBootstrap(id, raw)
	if err != nil || got != review || trust == nil || trust.InsecureSkipVerify || trust.ServerName != "127.0.0.1" {
		t.Fatal("valid provisional review rejected")
	}
	for i := range raw {
		missing := raw
		missing[i] = nil
		if _, _, err := validateStockPilotBootstrap(id, missing); err == nil {
			t.Fatalf("missing %d accepted", i)
		}
		changed := raw
		changed[i] = append(append([]byte{}, raw[i]...), ' ')
		if _, _, err := validateStockPilotBootstrap(id, changed); err == nil {
			t.Fatalf("changed %d accepted", i)
		}
	}
	if _, _, err := validateStockPilotBootstrap(strings.Repeat("f", 64), raw); err == nil {
		t.Fatal("foreign stock accepted")
	}
	// A completed review is not interchangeable with explicit provisional consent.
	if _, _, err := validateStockPilotBootstrap(id, [3][]byte{complete[0], complete[1], complete[4]}); err == nil {
		t.Fatal("wrong review domain accepted")
	}
}
func TestStockPilotBootstrapUsesDurableExistingProtocol(t *testing.T) {
	files := bootstrapFixture(t)
	id := strings.Repeat("a", 64)
	first, login := 0, 0
	responder := bootstrapResponder(new(int), false)
	lost := true
	exchange := func(ctx context.Context, method, path, token string, body any) ([]byte, error) {
		if !stockPilotBootstrapRoute(method, path, token, body) {
			t.Fatal("protocol exceeded pilot routes")
		}
		if path == "/api/v1/first-run" {
			first++
			var attempt bootstrapAttempt
			if files.read("bootstrap-attempt.json", &attempt) != nil || attempt.GarmID != id {
				t.Fatal("first-run before durable intent")
			}
			if lost {
				return nil, errors.New("lost response")
			}
		}
		if path == "/api/v1/auth/login" {
			login++
			if lost {
				return nil, errors.New("unavailable")
			}
		}
		return responder(ctx, method, path, token, body)
	}
	if _, _, err := bootstrapProtocol(context.Background(), files, id, exchange); err == nil {
		t.Fatal("unknown accepted")
	}
	var record bootstrapRecord
	if err := files.read("bootstrap-record.json", &record); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fabricated record")
	}
	// Explicit recovery uses the same persisted attempt, never another first-run.
	lost = false
	record, _, err := bootstrapProtocol(context.Background(), files, id, exchange)
	if err != nil || record.UserID != "" || !stockUUID(record.ControllerID) || first != 1 || login != 2 {
		t.Fatalf("recovery: %v first=%d login=%d", err, first, login)
	}
	if _, _, err := bootstrapProtocol(context.Background(), files, id, exchange); err != nil || first != 1 {
		t.Fatal("completed invocation repeated first-run")
	}
}
func TestStockPilotBootstrapRejectsRoutesAndCancellationBeforeRuntime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []context.Context{nil, ctx} {
		if bootstrapStockReadPilot(c) == nil {
			t.Fatal("invalid context")
		}
		var held *retainedStockRead
		if conn, err := held.dialStockAPI(c); err == nil || conn != nil {
			t.Fatal("nil dial accepted")
		}
	}
	for _, item := range []struct {
		method, path, token string
		body                any
	}{
		{http.MethodPost, "/api/v1/first-run?retry=true", "", struct{}{}},
		{http.MethodPut, "/api/v1/pools/11111111-1111-4111-8111-111111111111", "token", struct{}{}},
		{http.MethodGet, "/api/v1/instances", "token", nil},
		{http.MethodPost, "/api/v1/auth/login", "token", struct{}{}},
		{http.MethodGet, "/api/v1/controller", "", nil},
		{http.MethodGet, "/api/v1/controller", "token", struct{}{}},
	} {
		if stockPilotBootstrapRoute(item.method, item.path, item.token, item.body) {
			t.Fatal("forbidden route accepted")
		}
		if _, err := stockPilotBootstrapExchange(context.Background(), nil, nil, item.method, item.path, item.token, item.body); err == nil {
			t.Fatal("missing private prerequisites accepted")
		}
	}
}

func TestStockPilotBootstrapMatchesCompletedLeafGrammar(t *testing.T) {
	id, complete := stockAuthFixture(t)
	var user credential
	_ = json.Unmarshal(complete[4], &user)
	for _, tc := range []struct {
		name  string
		ca    bool
		usage x509.ExtKeyUsage
		ip    string
	}{
		{"CA", true, x509.ExtKeyUsageServerAuth, "127.0.0.1"},
		{"any-usage", false, x509.ExtKeyUsageAny, "127.0.0.1"},
		{"client-only", false, x509.ExtKeyUsageClientAuth, "127.0.0.1"},
		{"wrong-name", false, x509.ExtKeyUsageServerAuth, "127.0.0.2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pub, key, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), BasicConstraintsValid: true, IsCA: tc.ca, IPAddresses: []net.IP{net.ParseIP(tc.ip)}, ExtKeyUsage: []x509.ExtKeyUsage{tc.usage}}
			der, err := x509.CreateCertificate(rand.Reader, cert, cert, pub, key)
			if err != nil {
				t.Fatal(err)
			}
			review := stockPilotBootstrapReview{"stock-pilot-bootstrap/v1", id, stockBindingDigest(der), credentialHash(user)}
			raw, _ := json.Marshal(review)
			if _, _, err := validateStockPilotBootstrap(id, [3][]byte{raw, der, complete[4]}); err == nil {
				t.Fatal("completed-binding-incompatible leaf accepted before setup")
			}
		})
	}
}
