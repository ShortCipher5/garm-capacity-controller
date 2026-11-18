package stockidentity

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPendingInstanceObservationRejectsIdentityAmbiguity(t *testing.T) {
	pool := "11111111-1111-4111-8111-111111111111"
	body := `{"id":"33333333-3333-4333-8333-333333333333","name":"runner","pool_id":"11111111-1111-4111-8111-111111111111","provider_name":"stock","agent_id":123,"status":"creating","runner_status":"pending"}`
	cases := []string{body, strings.Replace(body, `"agent_id":123`, `"agent_id":0`, 1), strings.Replace(body, `"name":"runner"`, `"name":"runner","Name":"other"`, 1), strings.Replace(body, `"status":"creating"`, `"status":"running"`, 1), strings.Replace(body, `"runner_status":"pending"`, `"runner_status":"idle"`, 1), strings.Replace(body, `"pool_id":"`+pool+`"`, `"pool_id":"22222222-2222-4222-8222-222222222222"`, 1), strings.Replace(body, `"provider_name":"stock"`, `"provider_name":"other"`, 1), strings.Replace(body, `"name":"runner"`, `"name":"runner","provider_id":"already-native"`, 1), strings.Replace(body, `"name":"runner"`, `"name":"runner","scale_set_id":1`, 1), body + `{}`}
	for i, raw := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer admin" {
					t.Error("wrong source credential")
				}
				if r.URL.Path == "/api/v1/pools/"+pool {
					fmt.Fprint(w, `{"id":"11111111-1111-4111-8111-111111111111","repo_id":"22222222-2222-4222-8222-222222222222","provider_name":"stock","flavor":"small","image":"image","os_type":"linux","os_arch":"amd64"}`)
					return
				}
				if r.URL.Path != "/api/v1/instances/runner" {
					t.Error("wrong instance path")
				}
				fmt.Fprint(w, raw)
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			observation, e := ObserveInstance(context.Background(), Configuration{Endpoint: server.URL, Token: "admin", Provider: "stock", Pool: pool, TLS: &tls.Config{RootCAs: roots}}, "runner")
			if i == 0 {
				if e != nil || !observation.Valid() || observation.Facts().AgentID != 123 {
					t.Fatal(e)
				}
			} else if e == nil || observation.Valid() {
				t.Fatal("unqualified pending identity accepted")
			}
		})
	}
	if (InstanceObservation{}).Valid() {
		t.Fatal("zero observation authoritative")
	}
}

func TestManagedPendingInstanceUsesScopedCollectionAndSource(t *testing.T) {
	const pool = "11111111-1111-4111-8111-111111111111"
	const controller = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	row := `{"id":"33333333-3333-4333-8333-333333333333","name":"runner","pool_id":"11111111-1111-4111-8111-111111111111","provider_name":"stock","agent_id":123,"status":"creating","runner_status":"pending"}`
	for _, tc := range []struct {
		name, rows string
		drift      bool
		ok         bool
	}{
		{"valid", "[" + row + "]", false, true},
		{"missing", "[]", false, false},
		{"duplicate", "[" + row + "," + row + "]", false, false},
		{"alias", "[" + strings.Replace(row, `"name":"runner"`, `"name":"runner","Name":"other"`, 1) + "]", false, false},
		{"drift", "[" + row + "]", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.Header.Get("Authorization") != "" || r.TLS == nil || len(r.TLS.PeerCertificates) != 1 {
					t.Error("unscoped transport")
				}
				source := strings.Repeat("a", 64)
				if tc.drift && r.URL.Path == "/api/v1/instances" {
					source = strings.Repeat("b", 64)
				}
				w.Header().Set("X-Capacity-Source-ID", source)
				w.Header().Set("X-Capacity-Controller-ID", controller)
				switch r.URL.Path {
				case "/api/v1/pools/" + pool:
					fmt.Fprint(w, `{"id":"11111111-1111-4111-8111-111111111111","repo_id":"22222222-2222-4222-8222-222222222222","provider_name":"stock","flavor":"small","image":"image","os_type":"linux","os_arch":"amd64"}`)
				case "/api/v1/instances":
					fmt.Fprint(w, tc.rows)
				default:
					t.Error("unapproved path")
					w.WriteHeader(404)
				}
			}))
			server.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
			server.StartTLS()
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			pin := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
			c := Configuration{Endpoint: server.URL, Provider: "stock", Pool: pool, ManagedControllerID: controller, ManagedServerKeyPin: hex.EncodeToString(pin[:]), TLS: &tls.Config{RootCAs: roots, Certificates: server.TLS.Certificates}}
			got, e := ObserveInstance(context.Background(), c, "runner")
			if tc.ok {
				if e != nil || !got.Valid() || got.Facts().AgentID != 123 {
					t.Fatal(e)
				}
			} else if e == nil || got.Valid() {
				t.Fatal("unqualified managed instance accepted")
			}
		})
	}
}
