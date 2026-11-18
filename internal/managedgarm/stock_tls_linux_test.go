package managedgarm

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStockTLSUsesExactLeafAndNormalHostnameVerification(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer server.Close()
	raw := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	sum := sha256.Sum256(server.Certificate().Raw)
	pin := hex.EncodeToString(sum[:])
	trust, e := stockTrust(raw, pin)
	if e != nil {
		t.Fatal(e)
	}
	if trust.InsecureSkipVerify || trust.ServerName != "127.0.0.1" || trust.VerifyConnection == nil {
		t.Fatal("missing peer authentication")
	}
	transport := &http.Transport{TLSClientConfig: trust, Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	response, e := client.Get(server.URL)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if _, e = stockTrust(raw, strings.Repeat("f", 64)); e == nil {
		t.Fatal("wrong immutable pin accepted")
	}
	if _, e = stockTrust(append(raw, raw...), pin); e == nil {
		t.Fatal("ambiguous trust file accepted")
	}
	otherLeaf := *server.Certificate()
	otherLeaf.Raw = []byte("different authenticated leaf")
	if trust.VerifyConnection(tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{server.Certificate()}}, PeerCertificates: []*x509.Certificate{&otherLeaf}}) == nil {
		t.Fatal("pin callback accepted different leaf")
	}
}
