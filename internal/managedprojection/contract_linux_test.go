package managedprojection_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/managedboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedgarm"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedprojection"
)

func TestProjectionBoundsAndInputOwnership(t *testing.T) {
	providers := map[string]bool{"native": true}
	repositories := map[string]bool{"repo": true}
	raw := []byte(`[{"id":"pool","provider_name":"native","repo_id":"repo","tags":[{"name":"linux","secret":"hidden"}]}]`)
	before := bytes.Clone(raw)
	out, err := managedprojection.Demand(raw, "/api/v1/pools", providers, repositories)
	if err != nil || string(out) != `[{"id":"pool","provider_name":"native","repo_id":"repo","tags":[{"name":"linux"}]}]` {
		t.Fatalf("projection: %s %v", out, err)
	}
	out[0] = '!'
	if !bytes.Equal(raw, before) || len(providers) != 1 || !providers["native"] || len(repositories) != 1 || !repositories["repo"] {
		t.Fatal("projection mutated input")
	}
	oversized := []byte(strings.Repeat(" ", 4<<20+1))
	if _, err := managedprojection.Instances(oversized, providers); err != managedgarm.ErrBoundary {
		t.Fatal("inventory bound changed")
	}
	if _, err := managedprojection.Demand(oversized, "/api/v1/jobs", providers, repositories); err != managedgarm.ErrBoundary {
		t.Fatal("demand bound changed")
	}
	if _, err := managedprojection.Demand([]byte(`[]`), "/api/v1/auth/login", providers, repositories); err != managedgarm.ErrBoundary {
		t.Fatal("unsupported projection accepted")
	}
	jobs, err := managedprojection.Demand([]byte(`[{"repo_id":"repo","status":"queued","labels":["linux"],"secret":"hidden"},{"repo_id":"foreign","status":"queued"}]`), "/api/v1/jobs", providers, repositories)
	if err != nil || string(jobs) != `[{"labels":["linux"],"repo_id":"repo","status":"queued"}]` {
		t.Fatalf("jobs: %s %v", jobs, err)
	}
}

func TestBoundarySentinelIdentityPreserved(t *testing.T) {
	if managedgarm.ErrBoundary != managedboundary.ErrBoundary || managedgarm.ErrBoundary.Error() != "managed stock GARM boundary not established" {
		t.Fatal("boundary error identity changed")
	}
}
