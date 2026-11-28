package incusapi

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

const guestMetrics = `# TYPE incus_cpu_seconds_total counter
incus_cpu_seconds_total{cpu="0",mode="user",name="runner-one",project="default",type="container"} 72
incus_cpu_seconds_total{cpu="0",mode="system",name="runner-one",project="default",type="container"} 29
incus_cpu_seconds_total{cpu="0",mode="idle",name="runner-one",project="default",type="container"} 543
incus_memory_MemTotal_bytes{name="runner-one",project="default",type="container"} 1e9
incus_memory_MemFree_bytes{name="runner-one",project="default",type="container"} 6e8
incus_internal_unknown{irrelevant="SENTINEL"} 4
`

func TestMetricsObserverUsesOnlyPinnedGuestTelemetry(t *testing.T) {
	var seen atomic.Int32
	var redirect atomic.Bool
	srv, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/1.0/metrics?project=default" {
			t.Errorf("unexpected metrics request: %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if redirect.Load() {
			http.Redirect(w, r, "/1.0/instances", http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprint(w, guestMetrics)
	}))
	leaf, err := x509.ParseCertificate(srv.TLS.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	makeObserver := func(pin string) *MetricsObserver {
		t.Helper()
		m, err := NewMetricsObserver(srv.URL, "default", pin, credentials)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(m.Close)
		return m
	}
	bad := makeObserver(strings.Repeat("0", 64))
	if _, err := bad.ReadGuestTelemetry(context.Background()); !errors.Is(err, ErrTransport) || seen.Load() != 0 {
		t.Fatalf("wrong pin reached HTTP or succeeded: %v", err)
	}
	m := makeObserver(fmt.Sprintf("%x", sha256.Sum256(leaf.RawSubjectPublicKeyInfo)))
	v, err := m.ReadGuestTelemetry(context.Background())
	if err != nil || len(v) != 1 || v[0].Name != "runner-one" || v[0].CPUUsedSeconds == nil || *v[0].CPUUsedSeconds != 101 ||
		v[0].MemoryUsedBytes == nil || *v[0].MemoryUsedBytes != 400000000 {
		t.Fatalf("unexpected projected observation: %v %v", v, err)
	}
	redirect.Store(true)
	if _, err := m.ReadGuestTelemetry(context.Background()); !errors.Is(err, ErrTransport) || seen.Load() != 2 {
		t.Fatalf("redirect was accepted or followed: %v", err)
	}
}

func TestGuestMetricsProjectionBoundaries(t *testing.T) {
	for _, s := range []string{
		strings.Replace(guestMetrics, `project="default"`, `project="other"`, 1),
		strings.Replace(guestMetrics, `project="default"`, `project="default",project="default"`, 1),
		strings.Replace(guestMetrics, " 72\n", " NaN\n", 1),
		guestMetrics + strings.Split(guestMetrics, "\n")[1] + "\n",
	} {
		if rows, err := parseGuestTelemetry([]byte(s), "default"); !errors.Is(err, ErrResponse) || len(rows) != 0 {
			t.Fatalf("invalid metrics yielded an observation: %v", err)
		}
	}
	onlyMem := strings.Replace(guestMetrics, `mode="user"`, `mode="other"`, 1)
	if rows, err := parseGuestTelemetry([]byte(onlyMem), "default"); err != nil || len(rows) != 1 || rows[0].CPUUsedSeconds != nil || rows[0].MemoryUsedBytes == nil {
		t.Fatalf("missing CPU pair did not stay unobserved: %v", err)
	}
}

func TestMetricsObserverVerifiesExplicitDNSNameWhenDialingIP(t *testing.T) {
	var seen atomic.Int32
	const dns = "native-incus-host"
	srv, trust := tlsFixtureWithDNSName(t, dns, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/1.0/metrics?project=default" || r.TLS.ServerName != dns {
			t.Errorf("wrong metrics request or verified server name: %q", r.TLS.ServerName)
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, guestMetrics)
	}))
	leaf, err := x509.ParseCertificate(srv.TLS.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pin := fmt.Sprintf("%x", sha256.Sum256(leaf.RawSubjectPublicKeyInfo))
	makeObserver := func(name, serverPin string) *MetricsObserver {
		t.Helper()
		m, err := NewMetricsObserverWithServerName(srv.URL, "default", serverPin, name, trust)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(m.Close)
		return m
	}
	ctx := context.Background()
	for _, bad := range []*MetricsObserver{
		makeObserver("", pin), makeObserver("wrong-host", pin), makeObserver(dns, strings.Repeat("0", 64)),
	} {
		if _, err := bad.ReadGuestTelemetry(ctx); !errors.Is(err, ErrTransport) || seen.Load() != 0 {
			t.Fatal("unverified IP, DNS name or SPKI reached HTTP")
		}
	}
	if any, err := NewMetricsObserverWithServerName("https://existing-dns", "default", pin, dns, trust); any != nil || !errors.Is(err, ErrConfiguration) {
		t.Fatal("explicit alternate name allowed for a DNS dial endpoint")
	}
	if any, err := NewMetricsObserverWithServerName(srv.URL, "default", pin, "*.example.com", trust); any != nil || !errors.Is(err, ErrConfiguration) {
		t.Fatal("accepted a wildcard expected name")
	}
	if got, err := makeObserver(dns, pin).ReadGuestTelemetry(ctx); err != nil || len(got) != 1 || seen.Load() != 1 {
		t.Fatalf("pinned verified native DNS failed: observations=%d err=%v", len(got), err)
	}
}
