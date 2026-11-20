package managedgarm

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func serveMaterialFixture(t *testing.T) ([5][]byte, stockReadAuthBinding, []byte) {
	t.Helper()
	now := time.Now()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, e := x509.CreateCertificate(rand.Reader, ca, ca, pub, key)
	if e != nil {
		t.Fatal(e)
	}
	issue := func(serial int64, usage x509.ExtKeyUsage) ([]byte, []byte) {
		pub, k, _ := ed25519.GenerateKey(rand.Reader)
		c := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, ExtKeyUsage: []x509.ExtKeyUsage{usage}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature}
		der, e := x509.CreateCertificate(rand.Reader, c, ca, pub, key)
		if e != nil {
			t.Fatal(e)
		}
		pk, e := x509.MarshalPKCS8PrivateKey(k)
		if e != nil {
			t.Fatal(e)
		}
		return der, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk})
	}
	server, private := issue(2, x509.ExtKeyUsageServerAuth)
	client, _ := issue(3, x509.ExtKeyUsageClientAuth)
	stock, _ := issue(4, x509.ExtKeyUsageServerAuth)
	gateway, _ := issue(5, x509.ExtKeyUsageServerAuth)
	ingress, _ := issue(6, x509.ExtKeyUsageServerAuth)
	spki := func(der []byte) string {
		c, e := x509.ParseCertificate(der)
		if e != nil {
			t.Fatal(e)
		}
		return stockBindingDigest(c.RawSubjectPublicKeyInfo)
	}
	auth := stockReadAuthBinding{review: stockReadAuthReview{GarmID: strings.Repeat("a", 64), LeafSHA256: stockBindingDigest(stock)}}
	auth.record.ControllerID = "11111111-1111-4111-8111-111111111111"
	review := stockReadServeReview{Version: "stock-read-serve/v1", GarmID: auth.review.GarmID, ControllerID: auth.record.ControllerID, CASHA256: stockBindingDigest(caDER), ServerSHA256: stockBindingDigest(server), ClientSHA256: stockBindingDigest(client), GatewaySPKI: spki(gateway), IngressSPKI: spki(ingress)}
	raw, _ := json.Marshal(review)
	return [5][]byte{raw, caDER, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server}), private, client}, auth, stock
}
func TestStockReadServeSyntheticRoleContract(t *testing.T) {
	raw, auth, stock := serveMaterialFixture(t)
	m, e := validateStockReadServeMaterial(raw, auth, stock)
	if e != nil {
		t.Fatal(e)
	}
	if m.tls.MinVersion != tls.VersionTLS13 || m.tls.ClientAuth != tls.RequireAndVerifyClientCert || m.tls.InsecureSkipVerify || m.tls.GetConfigForClient != nil || m.commitment == "" {
		t.Fatal("TLS boundary changed")
	}
	client, _ := x509.ParseCertificate(raw[4])
	if m.tls.VerifyConnection(tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{client}}, PeerCertificates: []*x509.Certificate{client}}) != nil {
		t.Fatal("reviewed leaf rejected")
	}
	if m.tls.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{client}}) == nil {
		t.Fatal("unverified leaf accepted")
	}
	other, _ := x509.ParseCertificate(stock)
	if m.tls.VerifyConnection(tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{other}}, PeerCertificates: []*x509.Certificate{other}}) == nil {
		t.Fatal("wrong role accepted")
	}
	for _, mode := range []string{"controller", "garm", "ca-pin", "client-pin", "server-pin", "missing-role", "shared-role", "stock-role", "client-eku", "duplicate-key", "unknown-field"} {
		t.Run(mode, func(t *testing.T) {
			copy := raw
			var r stockReadServeReview
			json.Unmarshal(raw[0], &r)
			switch mode {
			case "controller":
				r.ControllerID = "22222222-2222-4222-8222-222222222222"
			case "garm":
				r.GarmID = strings.Repeat("b", 64)
			case "ca-pin":
				r.CASHA256 = strings.Repeat("b", 64)
			case "client-pin":
				r.ClientSHA256 = strings.Repeat("b", 64)
			case "server-pin":
				r.ServerSHA256 = strings.Repeat("b", 64)
			case "missing-role":
				r.GatewaySPKI = ""
			case "shared-role":
				r.GatewaySPKI = r.IngressSPKI
			case "stock-role":
				r.GatewaySPKI = stockBindingDigest(other.RawSubjectPublicKeyInfo)
			case "client-eku":
				copy[4] = stock
				r.ClientSHA256 = stockBindingDigest(stock)
			case "duplicate-key":
				copy[3] = nil
			}
			copy[0], _ = json.Marshal(r)
			if mode == "unknown-field" {
				copy[0] = append(copy[0][:len(copy[0])-1], []byte(`,"Scope":"all"}`)...)
			}
			if _, e := validateStockReadServeMaterial(copy, auth, stock); e == nil {
				t.Fatal("unreviewed role accepted")
			}
		})
	}
}
func TestStockReadServeInvalidContextNoRuntime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []context.Context{nil, ctx} {
		if serveStockReadOnly(c) == nil {
			t.Fatal("invalid context accepted")
		}
	}
	var empty *stockReadSession
	if empty.ControllerID() != "" || (&stockReadSession{}).ControllerID() != "" {
		t.Fatal("unverified controller exposed")
	}
}

func TestStockReadServeTrustOwnsClearedInput(t *testing.T) {
	raw, auth, stock := serveMaterialFixture(t)
	material, err := validateStockReadServeMaterial(raw, auth, stock)
	if err != nil {
		t.Fatal(err)
	}
	clientDER := append([]byte(nil), raw[4]...)
	for _, v := range raw {
		clear(v)
	}
	clear(stock)
	client, err := x509.ParseCertificate(clientDER)
	if err != nil {
		t.Fatal(err)
	}
	chains, err := client.Verify(x509.VerifyOptions{Roots: material.tls.ClientCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	if err != nil {
		t.Fatal("loader cleanup corrupted retained trust", err)
	}
	if material.tls.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{client}, VerifiedChains: chains}) != nil {
		t.Fatal("owned leaf rejected after cleanup")
	}
	if _, err := x509.ParseCertificate(material.tls.Certificates[0].Certificate[0]); err != nil {
		t.Fatal("loader cleanup corrupted serving leaf", err)
	}
}

func TestStockReadOwnedServiceNoRuntimeOnInvalidLifecycle(t *testing.T) {
	if _, e := newStockReadService(nil); e == nil {
		t.Fatal("nil context")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := newStockReadService(ctx); e == nil {
		t.Fatal("canceled constructor")
	}
	var nilOwner *stockReadService
	nilOwner.Close()
	if nilOwner.Run() == nil {
		t.Fatal("nil run")
	}
	owner := &stockReadService{}
	owner.Close()
	owner.Close()
	if owner.Run() == nil {
		t.Fatal("closed incomplete run")
	}
}

func TestStockReadOwnedCloseJoinsStartedLifetime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	owner := &stockReadService{ctx: ctx, cancel: cancel, started: true, done: make(chan struct{})}
	returned := make(chan struct{})
	go func() { owner.Close(); close(returned) }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("close did not cancel")
	}
	select {
	case <-returned:
		t.Fatal("close returned before run joined")
	default:
	}
	close(owner.done)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("close did not join")
	}
	owner.Close()
	if owner.Run() == nil {
		t.Fatal("closed owner reused")
	}
}
