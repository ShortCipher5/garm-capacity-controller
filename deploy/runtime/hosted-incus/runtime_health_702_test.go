package hostedincus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

const (
	runtime702Controller = "11111111-1111-4111-8111-111111111111"
	runtime702Pool       = "22222222-2222-4222-8222-222222222222"
)

// TestHostedIncusRuntimeHealth702 is an opt-in client/daemon wire-format
// experiment. Independently supplied Unix fixture metadata names the running
// guest; only the scoped mTLS client performs the actual health observation.
// This cannot attribute a resource sample to a GitHub job or prove continuity.
func TestHostedIncusRuntimeHealth702(t *testing.T) {
	metadata := os.Getenv("GCC_HOSTED_INCUS_RUNTIME_METADATA")
	if metadata == "" {
		t.Skip("requires the independent Incus 7.0.2 hosted fixture")
	}
	dir, endpoint := os.Getenv("GCC_HOSTED_INCUS_AUTH_DIR"), os.Getenv("GCC_HOSTED_INCUS_AUTH_ENDPOINT")
	project, guest := os.Getenv("GCC_HOSTED_INCUS_AUTH_PROJECT"), os.Getenv("GCC_HOSTED_INCUS_AUTH_GUEST")
	temp := os.Getenv("RUNNER_TEMP")
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" ||
		endpoint != "https://127.0.0.1:8443" || !strings.HasPrefix(project, "garm-ci-project-") ||
		!strings.HasPrefix(guest, "garm-ci-") || !filepath.IsAbs(temp) ||
		filepath.Dir(dir) != temp || metadata != filepath.Join(dir, "runtime-instance.json") {
		t.Fatal("unsafe hosted Incus runtime fixture")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		t.Fatal("runtime fixture directory is not private")
	}
	info, err = os.Lstat(metadata)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 64<<10 {
		t.Fatal("runtime fixture metadata is not a private bounded file")
	}
	data, err := os.ReadFile(metadata)
	if err != nil {
		t.Fatal("could not read independently recorded fixture metadata")
	}
	var row struct {
		Name      string            `json:"name"`
		Project   string            `json:"project"`
		Type      string            `json:"type"`
		Status    string            `json:"status"`
		CreatedAt time.Time         `json:"created_at"`
		Config    map[string]string `json:"config"`
	}
	if err := json.Unmarshal(data, &row); err != nil || row.Name != guest || row.Project != project ||
		row.Type != "container" || row.Status != "Running" || row.CreatedAt.IsZero() ||
		row.Config["user.runner-controller-id"] != runtime702Controller ||
		row.Config["user.runner-pool-id"] != runtime702Pool {
		t.Fatal("independently recorded runtime fixture metadata did not match the disposable guest")
	}
	expected := incusapi.RunnerRuntimeExpected{
		RunnerRootVolumeExpected: incusapi.RunnerRootVolumeExpected{
			Name: guest, ControllerID: runtime702Controller, PoolID: runtime702Pool,
		},
		UUID: row.Config["volatile.uuid"], Generation: row.Config["volatile.uuid.generation"], CreatedAt: row.CreatedAt,
	}
	pem, err := os.ReadFile(filepath.Join(dir, "daemon.pem"))
	roots := x509.NewCertPool()
	if err != nil || !roots.AppendCertsFromPEM(pem) {
		t.Fatal("could not load fixture daemon public trust")
	}
	leaf, err := tls.LoadX509KeyPair(filepath.Join(dir, "allowed.pem"), filepath.Join(dir, "allowed.key"))
	if err != nil {
		t.Fatal("could not load project-scoped fixture client identity")
	}
	client, err := incusapi.New(endpoint, project, &tls.Config{
		RootCAs: roots, Certificates: []tls.Certificate{leaf}, MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("construct runtime read client: %v", err)
	}
	t.Cleanup(client.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	var empty incusapi.RunnerRuntimeHealth
	if result, err := client.ReadRunnerRuntimeHealth(ctx, expected); !errors.Is(err, incusapi.ErrConfiguration) || result != empty {
		t.Fatal("unpinned client returned a runtime observation")
	}
	identity, err := client.PhysicalIdentity(ctx)
	if err != nil || !identity.Valid() || identity.ServerVersion() != "7.0.2" {
		t.Fatalf("expected authenticated standalone Incus 7.0.2 runtime: %v", err)
	}
	if err := client.PinPhysicalIdentity(identity); err != nil {
		t.Fatalf("install fixture daemon identity pin: %v", err)
	}
	before := time.Now()
	result, err := client.ReadRunnerRuntimeHealth(ctx, expected)
	if err != nil || result.CPUCumulativeNanoseconds == nil || *result.CPUCumulativeNanoseconds <= 0 ||
		result.MemoryCurrentBytes == nil || *result.MemoryCurrentBytes <= 0 ||
		result.SampledAt.Before(before) || result.SampledAt.After(time.Now()) {
		t.Fatalf("real native runtime did not provide a bounded CPU/current RAM observation: %v", err)
	}
	if result.MemoryCgroupPeakBytes != nil && *result.MemoryCgroupPeakBytes < *result.MemoryCurrentBytes {
		t.Fatal("native cgroup memory peak falls below observed current RAM")
	}
	for _, change := range []struct {
		label string
		edit  func(*incusapi.RunnerRuntimeExpected)
	}{
		{"creation date", func(x *incusapi.RunnerRuntimeExpected) { x.CreatedAt = x.CreatedAt.Add(-time.Second) }},
		{"generation", func(x *incusapi.RunnerRuntimeExpected) {
			x.Generation = runtime702Controller
			if x.Generation == expected.Generation {
				x.Generation = runtime702Pool
			}
		}},
		{"incarnation", func(x *incusapi.RunnerRuntimeExpected) {
			x.UUID = runtime702Controller
			if x.UUID == expected.UUID {
				x.UUID = runtime702Pool
			}
		}},
	} {
		wrong := expected
		change.edit(&wrong)
		if got, err := client.ReadRunnerRuntimeHealth(ctx, wrong); !errors.Is(err, incusapi.ErrResponse) || got != empty {
			t.Fatalf("wrong %s returned or failed to reject a runtime observation: %v", change.label, err)
		}
	}
	t.Log("PASS pinned real Incus 7.0.2 CPU/current RAM and wrong incarnation refusal; no GitHub job attribution")
}
