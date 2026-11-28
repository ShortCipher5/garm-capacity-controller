package hostedincus

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

func TestHostedIncusMetricsObserver702(t *testing.T) {
	dir := os.Getenv("GCC_HOSTED_INCUS_AUTH_DIR")
	if dir == "" {
		t.Skip("requires the disposable hosted Incus fixture")
	}
	endpoint, project, guest := os.Getenv("GCC_HOSTED_INCUS_AUTH_ENDPOINT"),
		os.Getenv("GCC_HOSTED_INCUS_AUTH_PROJECT"), os.Getenv("GCC_HOSTED_INCUS_AUTH_GUEST")
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" ||
		endpoint != "https://127.0.0.1:8443" || !strings.HasPrefix(project, "garm-ci-project-") || !strings.HasPrefix(guest, "garm-ci-") {
		t.Fatal("unsafe hosted Incus fixture selection")
	}
	ca, err := os.ReadFile(filepath.Join(dir, "daemon.pem"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	block, _ := pem.Decode(ca)
	if block == nil || block.Type != "CERTIFICATE" || !roots.AppendCertsFromPEM(ca) {
		t.Fatal("missing native daemon trust")
	}
	server, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	client, err := tls.LoadX509KeyPair(filepath.Join(dir, "metrics.pem"), filepath.Join(dir, "metrics.key"))
	if err != nil {
		t.Fatal(err)
	}
	pin := fmt.Sprintf("%x", sha256.Sum256(server.RawSubjectPublicKeyInfo))
	m, err := incusapi.NewMetricsObserver(endpoint, project, pin, &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{client}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	rows, err := m.ReadGuestTelemetry(ctx)
	if err != nil || len(rows) != 1 || rows[0].Name != guest || rows[0].CPUUsedSeconds == nil ||
		rows[0].MemoryUsedBytes == nil || *rows[0].MemoryUsedBytes == 0 || *rows[0].MemoryUsedBytes > 256<<20 {
		t.Fatalf("expected projected CPU and memory for the scoped native running fixture: rows=%d error=%v", len(rows), err)
	}
}
