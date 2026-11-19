package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/stockingress"
)

func TestManagedIngressSchemaFixtureWithoutListener(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/managed-control-plane/testdata/ingress.json")
	if err != nil {
		t.Fatal(err)
	}
	var c config
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		t.Fatal("invalid actual ingress schema")
	}
	if c.Listen != "0.0.0.0:8443" || c.Controller != "" || c.TLSCertificate == "" || c.TLSKey == "" || c.StockCertificate == "" || len(c.StockFingerprint) != 64 {
		t.Fatal("missing fixture inputs")
	}
	// An empty synthetic trust pool creates no credentials and verifies no peer.
	trust := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: x509.NewCertPool()}
	if _, err := stockingress.NewTLS(c.Upstream, c.Controller, trust); err != nil {
		t.Fatal(err)
	}
	for _, upstream := range []string{"", "http://127.0.0.1:9998", "https://example.invalid:9998", "https://127.0.0.1:9998/api"} {
		if _, err := stockingress.NewTLS(upstream, c.Controller, trust); err == nil {
			t.Fatal("invalid fixture upstream accepted")
		}
	}
	// No handler request, run(), certificate load or listener is invoked.
}
