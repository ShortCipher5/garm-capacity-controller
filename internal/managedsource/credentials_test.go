package managedsource

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCredentialReadExactProtocolAndLatchedDrift(t *testing.T) {
	const controller = "11111111-1111-4111-8111-111111111111"
	const pool = "22222222-2222-4222-8222-222222222222"
	var mode atomic.Int32
	var calls atomic.Int32
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/v1/managed/metadata/"+pool+"/runner/credentials" || r.Header.Get("Authorization") != "" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("wrong credential protocol")
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || body["instance_token"] != "instance-secret" {
			t.Error("wrong credential body")
		}
		source := strings.Repeat("a", 64)
		if mode.Load() == 1 {
			source = strings.Repeat("b", 64)
		}
		w.Header().Set(SourceHeader, source)
		w.Header().Set(ControllerHeader, controller)
		if mode.Load() == 2 {
			w.Header().Set("Location", "/v1/managed/metadata/"+pool+"/runner/credentials")
			w.WriteHeader(307)
			return
		}
		w.Write([]byte("original-secret-bytes"))
	}))
	s.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	s.StartTLS()
	defer s.Close()
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	pin := sha256.Sum256(s.Certificate().RawSubjectPublicKeyInfo)
	c, e := New(s.URL, hex.EncodeToString(pin[:]), controller, &tls.Config{RootCAs: roots, Certificates: s.TLS.Certificates})
	if e != nil {
		t.Fatal(e)
	}
	defer c.CloseIdleConnections()
	read := func() ([]byte, error) {
		return c.ReadCredential(context.Background(), pool, "runner", "credentials", "instance-secret")
	}
	if b, e := read(); e != nil || string(b) != "original-secret-bytes" || c.Identity() == "" {
		t.Fatal(e)
	}
	mode.Store(2)
	before := calls.Load()
	if b, e := read(); e == nil || len(b) != 0 || calls.Load() != before+1 {
		t.Fatal("redirect followed or secret exposed")
	}
	mode.Store(1)
	if b, e := read(); e == nil || len(b) != 0 || c.Identity() != "" {
		t.Fatal("drift accepted")
	}
	mode.Store(0)
	before = calls.Load()
	if _, e := read(); e == nil || calls.Load() != before {
		t.Fatal("revocation failed")
	}
	if _, e := c.ReadCredential(context.Background(), pool, "../escape", "credentials", "instance-secret"); e == nil {
		t.Fatal("path accepted")
	}
}
