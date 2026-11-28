package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
	"github.com/ShortCipher5/garm-capacity-controller/internal/observerview"
)

func TestObserverModeNeverFallsThrough(t *testing.T) {
	for _, tc := range []struct {
		args            []string
		selected, valid bool
	}{
		{[]string{"-config=normal"}, false, false},
		{[]string{"-observer-config=observer"}, true, true},
		{[]string{"-observer-config="}, true, false},
		{[]string{"-observer-config=", "-config=normal", "-allow-experimental-provisioning"}, true, false},
		{[]string{"-observer-config=observer", "-observer-config=", "-config=normal"}, true, false},
		{[]string{"-observer-config=observer", "-config="}, true, false},
		{[]string{"-observer-config=observer", "extra"}, true, false},
	} {
		flags := flag.NewFlagSet("observer-test", flag.ContinueOnError)
		observer := flags.String("observer-config", "", "")
		flags.String("config", "", "")
		flags.Bool("allow-experimental-provisioning", false, "")
		if err := flags.Parse(tc.args); err != nil {
			t.Fatal(err)
		}
		if selected, valid := observerModeSelection(flags, *observer); selected != tc.selected || valid != tc.valid {
			t.Fatalf("%v: got selected=%v, valid=%v", tc.args, selected, valid)
		}
	}
}

func TestStandaloneObserverUsesStockAndMetricsOverTLS(t *testing.T) {
	keys := credentials(t)
	leaf, err := x509.ParseCertificate(keys.server.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pin := fmt.Sprintf("%x", sha256.Sum256(leaf.RawSubjectPublicKeyInfo))
	controller := "11111111-1111-4111-8111-111111111111"
	var stockReads, metricsReads atomic.Int32
	stock := mutualServer(t, keys, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
			t.Error("unexpected stock authority")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.RequestURI() {
		case "/api/v1/pools", "/api/v1/jobs", "/api/v1/instances":
			stockReads.Add(1)
			w.Header().Set(managedsource.SourceHeader, strings.Repeat("a", 64))
			w.Header().Set(managedsource.ControllerHeader, controller)
			_, _ = io.WriteString(w, "[]")
		default:
			t.Error("unexpected stock route")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	metrics := mutualServer(t, keys, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/1.0/metrics?project=default" {
			t.Error("unexpected Incus route or method")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		metricsReads.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, `incus_memory_MemTotal_bytes{name="fixture",project="default",type="container"} 1000
incus_memory_MemFree_bytes{name="fixture",project="default",type="container"} 750
`)
	}))
	token := strings.Repeat("b", 64)
	tokenFile := filepath.Join(t.TempDir(), "dashboard.token")
	if err := os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cfg := observerDashboardConfig{
		Listen: listener.Addr().String(), ServerTLS: keys.serverFiles, TokenFile: tokenFile,
		Stock: observerStockConfig{
			Endpoint: stock.URL, Controller: controller, ServerKeyPin: pin, ClientTLS: keys.providerFiles,
			Providers: []string{"fixture"}, Repositories: []string{"22222222-2222-4222-8222-222222222222"},
		},
		Metrics: map[string]observerMetricsConfig{"fixture-host": {
			Endpoint: metrics.URL, Project: "default", ServerKeyPin: pin, ClientTLS: keys.otherFiles,
		}},
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serveObserverDashboard(ctx, cfg, listener) }()
	defer func() {
		cancel()
		if e := <-done; e != nil {
			t.Error(e)
		}
	}()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: keys.root}}}
	defer client.CloseIdleConnections()
	get := func(path, auth string) *http.Response {
		t.Helper()
		r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+cfg.Listen+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		res, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	page := get("/admin/", "")
	body, _ := io.ReadAll(page.Body)
	if page.StatusCode != http.StatusOK || !strings.Contains(string(body), "GARM Controller") || page.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("missing operator entry page or policy")
	}
	if get(observerview.Path, "").StatusCode != http.StatusUnauthorized || stockReads.Load()+metricsReads.Load() != 0 {
		t.Fatal("unauthenticated view reached upstream")
	}
	res := get(observerview.Path, token)
	var snapshot observerview.Snapshot
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&snapshot) != nil || snapshot.Stock == nil ||
		len(snapshot.Hosts) != 1 || len(snapshot.Hosts[0].Guests) != 1 ||
		snapshot.Hosts[0].Guests[0].MemoryUsedBytes == nil || *snapshot.Hosts[0].Guests[0].MemoryUsedBytes != 250 ||
		stockReads.Load() < 3 || metricsReads.Load() != 1 {
		t.Fatalf("standalone dashboard did not return both authenticated sources: HTTP %d", res.StatusCode)
	}
}
