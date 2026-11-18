package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExplicitStockPluginUsesSeparateCreateRoute(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/stock/create" {
			t.Error("wrong origin route")
		}
		_, _ = w.Write([]byte(`{"provider_id":"r","name":"r","os_type":"linux","os_arch":"amd64","status":"running"}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	write := func(n string, b []byte) string {
		p := filepath.Join(dir, n)
		if e := os.WriteFile(p, b, 0600); e != nil {
			t.Fatal(e)
		}
		return p
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	key, e := x509.MarshalPKCS8PrivateKey(server.TLS.Certificates[0].PrivateKey)
	if e != nil {
		t.Fatal(e)
	}
	tls := securefiles.TLSFiles{CA: write("ca", cert), Certificate: write("cert", cert), Key: write("key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))}
	config := configuration{Controller: "c", Gateway: server.URL, TLS: tls, StockTLS: &tls, CreateOrigin: "stock-provider-v1"}
	raw, _ := json.Marshal(config)
	p := write("config", raw)
	env := map[string]string{"GARM_COMMAND": "CreateInstance", "GARM_INTERFACE_VERSION": "v0.1.1", "GARM_PROVIDER_CONFIG_FILE": p, "GARM_CONTROLLER_ID": "c", "GARM_POOL_ID": "p"}
	var out bytes.Buffer
	code := run(context.Background(), environment(env), strings.NewReader(`{"name":"r","pool_id":"p","os_type":"linux","arch":"amd64"}`), &out)
	if code != 0 || calls != 1 {
		t.Fatalf("stock invocation %d calls %d output %s", code, calls, &out)
	}
	env["GARM_COMMAND"] = "AdmitInstance"
	out.Reset()
	if run(context.Background(), environment(env), strings.NewReader("{}"), &out) != 1 || calls != 1 {
		t.Fatal("stock mode allowed prepared command")
	}
}
