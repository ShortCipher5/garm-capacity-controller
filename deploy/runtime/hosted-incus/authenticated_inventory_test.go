package hostedincus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

// This is a read-only use of the application's real Incus client against the
// workflow's disposable daemon. Native certificate scope is not a read-only
// Incus ACL and the test deliberately makes no mutation request.
func TestHostedIncusAuthenticatedInventory(t *testing.T) {
	dir := os.Getenv("GCC_HOSTED_INCUS_AUTH_DIR")
	if dir == "" {
		t.Skip("requires the disposable hosted Incus fixture")
	}
	endpoint, project, guest := os.Getenv("GCC_HOSTED_INCUS_AUTH_ENDPOINT"),
		os.Getenv("GCC_HOSTED_INCUS_AUTH_PROJECT"), os.Getenv("GCC_HOSTED_INCUS_AUTH_GUEST")
	if os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || endpoint != "https://127.0.0.1:8443" ||
		!strings.HasPrefix(project, "garm-ci-project-") || !strings.HasPrefix(guest, "garm-ci-") {
		t.Fatal("unsafe hosted Incus fixture selection")
	}
	pem, err := os.ReadFile(filepath.Join(dir, "daemon.pem"))
	if err != nil {
		t.Fatalf("load disposable daemon trust: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		t.Fatal("no disposable daemon trust")
	}
	client := func(role, sourceProject string) *incusapi.Client {
		t.Helper()
		leaf, err := tls.LoadX509KeyPair(filepath.Join(dir, role+".pem"), filepath.Join(dir, role+".key"))
		if err != nil {
			t.Fatalf("load disposable %s identity: %v", role, err)
		}
		c, err := incusapi.New(endpoint, sourceProject, &tls.Config{
			RootCAs: roots, Certificates: []tls.Certificate{leaf}, MinVersion: tls.VersionTLS12,
		})
		if err != nil {
			t.Fatalf("construct %s client: %v", role, err)
		}
		t.Cleanup(c.Close)
		return c
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	allowed := client("allowed", project)
	rows, err := allowed.Instances(ctx)
	if err != nil || len(rows) != 1 || rows[0].Name != guest || rows[0].Status != 103 ||
		rows[0].CPUMillis != 1000 || rows[0].RAMMiB != 256 {
		t.Fatalf("project allocation read did not match the bounded running guest: count=%d error=%v", len(rows), err)
	}
	exact, err := allowed.Instance(ctx, guest)
	if err != nil || exact != rows[0] {
		t.Fatalf("exact and project allocation views did not agree: %v", err)
	}
	guests, err := allowed.Guests(ctx)
	if err != nil || len(guests) != 1 || guests[0].Name != guest || guests[0].CPUMillis != 1000 || guests[0].RAMMiB != 256 {
		t.Fatalf("read-only guest inventory did not match the bounded allocation: count=%d error=%v", len(guests), err)
	}
	if ops, err := allowed.Operations(ctx); err != nil || ops == nil {
		t.Fatalf("authenticated standalone operation inventory unavailable: %v", err)
	}

	untrusted := client("unknown", project)
	if rows, err := untrusted.Instances(ctx); !errors.Is(err, incusapi.ErrUnsupported) || rows != nil {
		t.Fatalf("untrusted identity yielded inventory: count=%d error=%v", len(rows), err)
	}
	outsideScope := client("wrong-project", project)
	if _, err := outsideScope.Instance(ctx, guest); !errors.Is(err, incusapi.ErrTransport) ||
		(!strings.Contains(err.Error(), "HTTP 403") && !strings.Contains(err.Error(), "HTTP 404")) {
		t.Fatalf("different-project identity was not denied access to the known guest: %v", err)
	}
	// List routes can hide foreign resources as an empty successful result.
	// Either response is acceptable so long as the known guest is not disclosed.
	if rows, err := outsideScope.Instances(ctx); len(rows) != 0 || (err != nil && !errors.Is(err, incusapi.ErrTransport)) {
		t.Fatalf("different-project identity exposed inventory or lacked a clean refusal: count=%d error=%v", len(rows), err)
	}
	t.Log("PASS real Incus TLS authentication, scoped guest/allocation/operation reads, and negative trust/scope controls")
}
