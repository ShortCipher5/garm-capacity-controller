package stockjit

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	common "github.com/cloudbase/garm-provider-common/params"
)

const runnerBody = `{"agentId":123,"agentName":"runner-one","gitHubUrl":"https://github.com/example/repo","ephemeral":true,"workFolder":"_work","ignoredSecret":"must-not-escape"}`

func fixture(t *testing.T, handler http.HandlerFunc) (*Client, common.BootstrapInstance, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/api/v1/metadata/credentials/runner" || r.Header.Get("Authorization") != "Bearer private-instance-token" {
			t.Error("unexpected stock metadata request")
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	policy := Policy{MetadataURL: server.URL + "/api/v1/metadata", ControllerID: "controller", PoolID: "pool", GitHubURL: "https://github.com/example/repo"}
	client, err := New(policy, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client, common.BootstrapInstance{Name: "runner-one", PoolID: "pool", RepoURL: policy.GitHubURL, MetadataURL: policy.MetadataURL, JitConfigEnabled: true, InstanceToken: "private-instance-token"}, calls
}
func TestStockMetadataRegistrationIdentityWithoutSecrets(t *testing.T) {
	client, b, calls := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "octet-stream")
		w.Write([]byte(runnerBody))
	})
	observation, err := client.Observe(context.Background(), "controller", b)
	if err != nil || !observation.Valid() || observation.Record().RunnerID != 123 || calls.Load() != 1 {
		t.Fatal(observation, err)
	}
	data, err := json.Marshal(observation.Record())
	if err != nil || strings.Contains(string(data), "private-instance-token") || strings.Contains(string(data), "must-not-escape") {
		t.Fatal("credential material escaped")
	}
	if (Observation{}).Valid() || (Observation{}).Record().RunnerID != 0 {
		t.Fatal("unverified record accepted")
	}
}
func TestStockPolicyRejectsBeforeTokenTransmission(t *testing.T) {
	client, b, calls := fixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected network") })
	cases := []common.BootstrapInstance{b, b, b, b, b, b}
	cases[0].JitConfigEnabled = false
	cases[1].MetadataURL = "https://attacker.invalid/api/v1/metadata"
	cases[2].RepoURL = "https://github.com/other/repo"
	cases[3].PoolID = "other"
	cases[4].InstanceToken = "bad\r\nHeader: value"
	cases[5].Name = ""
	for _, candidate := range cases {
		if _, err := client.Observe(context.Background(), "controller", candidate); !errors.Is(err, ErrPolicy) {
			t.Fatal("unsupported request permitted", err)
		}
	}
	if _, err := client.Observe(context.Background(), "other-controller", b); !errors.Is(err, ErrPolicy) {
		t.Fatal("controller mismatch permitted")
	}
	if calls.Load() != 0 {
		t.Fatal("policy failure transmitted credential")
	}
}
func TestStockMetadataRejectsMalformedAndConflictingResponses(t *testing.T) {
	for _, body := range []string{
		strings.Replace(runnerBody, `"ephemeral":true`, `"ephemeral":false`, 1),
		strings.Replace(runnerBody, `"agentId":123`, `"agentId":0`, 1),
		strings.Replace(runnerBody, `"agentId":123`, `"agentId":123,"AgentId":456`, 1),
		strings.Replace(runnerBody, `"agentId":123`, `"agentId":123.0`, 1),
		strings.Replace(runnerBody, `"agentId":123`, `"agentId":null`, 1),
		strings.Replace(runnerBody, `runner-one`, `other-runner`, 1),
		strings.Replace(runnerBody, `example/repo`, `other/repo`, 1),
		`{}`, runnerBody + `{}`, strings.Repeat("x", 65537),
	} {
		t.Run(body[:min(len(body), 30)], func(t *testing.T) {
			client, b, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) })
			if _, err := client.Observe(context.Background(), "controller", b); !errors.Is(err, ErrObservation) {
				t.Fatal("invalid observation accepted", err)
			}
		})
	}
}
func TestStockMetadataDoesNotRedirectRetryOrLeakErrors(t *testing.T) {
	var redirected atomic.Int32
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer destination.Close()
	for _, status := range []int{302, 401, 404, 500} {
		client, b, calls := fixture(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", destination.URL+"/private-instance-token")
			w.WriteHeader(status)
			w.Write([]byte("private-instance-token"))
		})
		_, err := client.Observe(context.Background(), "controller", b)
		if !errors.Is(err, ErrObservation) || calls.Load() != 1 || strings.Contains(err.Error(), "private-instance-token") {
			t.Fatal("retry/error leak", err)
		}
	}
	if redirected.Load() != 0 {
		t.Fatal("redirect followed")
	}
}
func TestStockMetadataRequiresVerifiedTLSAndExactEndpoint(t *testing.T) {
	policy := Policy{MetadataURL: "https://metadata.example/api/v1/metadata", ControllerID: "controller", PoolID: "pool", GitHubURL: "https://github.com/example/repo"}
	for _, trust := range []*tls.Config{nil, {InsecureSkipVerify: true}, {ServerName: "override.invalid"}} {
		if _, err := New(policy, trust); !errors.Is(err, ErrPolicy) {
			t.Fatal("unsafe TLS accepted")
		}
	}
	for _, endpoint := range []string{"http://metadata.example/api/v1/metadata", "https://user:secret@metadata.example/api/v1/metadata", "https://metadata.example/api/v1/metadata?x=y", "https://metadata.example/api/v1/metadata?", "https://metadata.example/api/v1/metadata#", "https://metadata.example/prefix/api/v1/metadata", "https://metadata.example/api/v1/metadata/"} {
		copy := policy
		copy.MetadataURL = endpoint
		if _, err := New(copy, &tls.Config{}); !errors.Is(err, ErrPolicy) {
			t.Fatal("unsupported endpoint accepted")
		}
	}
	client, b, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(runnerBody)) })
	untrusted, err := New(client.policy, &tls.Config{RootCAs: x509.NewCertPool()})
	if err != nil {
		t.Fatal(err)
	}
	defer untrusted.Close()
	if _, err := untrusted.Observe(context.Background(), "controller", b); !errors.Is(err, ErrObservation) {
		t.Fatal("untrusted TLS succeeded")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Observe(cancelled, "controller", b); !errors.Is(err, ErrObservation) {
		t.Fatal("cancelled lookup succeeded")
	}
}
