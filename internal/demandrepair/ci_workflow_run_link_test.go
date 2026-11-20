package demandrepair

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"testing"
	"time"
)

func workflowRunLinkFixture(t *testing.T, endpoint, host string) *CIHealthSource {
	t.Helper()
	repo := Repository{GarmID: "fixture", Host: host, Owner: "Example-Org", Name: "example.repo"}
	client, err := NewGitHub(endpoint, host, "unused", func(context.Context, string) (string, error) {
		t.Error("pure link generation requested credentials")
		return "", ErrObservation
	}, &tls.Config{RootCAs: x509.NewCertPool()}, []Repository{repo})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	source, err := NewCIHealthSource(client, []CIRepository{{Repository: repo, GitHubID: 9007199254740993}})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestWorkflowRunLinkUsesOnlyExactEnrolledSourceAnd64BitRun(t *testing.T) {
	s := workflowRunLinkFixture(t, "https://api.github.com", "github.com")
	const repo = 9007199254740993
	link, err := s.WorkflowRunLink(s.SourceID(), repo, math.MaxInt64)
	const want = "https://github.com/Example-Org/example.repo/actions/runs/9223372036854775807"
	if err != nil || link.URL() != want {
		t.Fatalf("did not construct documented run-level link: %v", err)
	}
	u, err := url.Parse(link.URL())
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		t.Fatalf("run link carried other destination data: %v", err)
	}
	for _, bad := range []struct {
		source      string
		repoID, run int64
	}{
		{"", repo, math.MaxInt64},
		{"other-source", repo, math.MaxInt64},
		{s.SourceID(), repo - 1, math.MaxInt64},
		{s.SourceID(), repo + 1, math.MaxInt64},
		{s.SourceID(), 0, math.MaxInt64},
		{s.SourceID(), repo, 0},
		{s.SourceID(), repo, -1},
	} {
		if got, err := s.WorkflowRunLink(bad.source, bad.repoID, bad.run); !errors.Is(err, ErrObservation) || got.URL() != "" {
			t.Fatalf("unscoped ID returned a URL: %v", err)
		}
	}
}

func TestWorkflowRunLinkNoAlternateOriginOrUnsafeSegment(t *testing.T) {
	const repoID = 9007199254740993
	for _, origin := range [][2]string{
		{"https://git.example/api/v3", "git.example"},
		{"https://api.github.com:443", "github.com"},
		{"https://api.github.com/api/v3", "github.com"},
		{"https://api-proxy.example", "github.com"},
		{"https://api.github.com", "github.com."},
	} {
		s := workflowRunLinkFixture(t, origin[0], origin[1])
		if got, err := s.WorkflowRunLink(s.SourceID(), repoID, 1); !errors.Is(err, ErrObservation) || got.URL() != "" {
			t.Fatal("unreviewed origin produced a link", err)
		}
	}
	for _, invalid := range []string{".", "..", "has space", "has/slash", "has:colon", "has?query", "has#fragment", "has%escape", "has\\slash", "has\x00control"} {
		s := workflowRunLinkFixture(t, "https://api.github.com", "github.com")
		for _, owner := range []bool{true, false} {
			r := s.scope[0]
			if owner {
				r.Owner = invalid
			} else {
				r.Name = invalid
			}
			// Simulate internally corrupted input consistently across all
			// lookup tables so the segment check itself must refuse it.
			s.scope[0], s.byGarm[r.GarmID], s.client.scope[r.GarmID] = r, r, r.Repository
			if got, err := s.WorkflowRunLink(s.SourceID(), repoID, 1); !errors.Is(err, ErrObservation) || got.URL() != "" {
				t.Fatal("unsafe segment produced a link", err)
			}
		}
	}
}

func TestPrepareObservedWorkflowRunLinkRequiresExactMintAndOriginalAge(t *testing.T) {
	s := workflowRunLinkFixture(t, "https://api.github.com", "github.com")
	shadow := workflowRunLinkFixture(t, "https://api.github.com", "github.com")
	now := time.Now()
	row := CIAttempt{
		source: s, repository: s.Repositories()[0], at: now.UTC(), minted: now,
		run: CIRun{WorkflowID: 2, RunID: math.MaxInt64, Attempt: 7, SourceURL: "ignored-fixture-link"},
	}
	if link, err := s.PrepareObservedWorkflowRunLink(t.Context(), row); err != nil || link.URL() != "https://github.com/Example-Org/example.repo/actions/runs/9223372036854775807" {
		t.Fatalf("documented run link not minted: %v", err)
	}
	expired := row
	expired.minted = now.Add(-ciAttemptLifetime)
	expired.at = expired.minted.UTC()
	changed := row
	changed.repository.GitHubID++
	noMono := row
	noMono.minted = noMono.minted.Round(0)
	for _, bad := range []CIAttempt{expired, changed, noMono, {}} {
		if got, err := s.PrepareObservedWorkflowRunLink(t.Context(), bad); !errors.Is(err, ErrObservation) || got.URL() != "" {
			t.Fatal("invalid proof directed a link", err)
		}
	}
	if shadow.SourceID() != s.SourceID() {
		t.Fatal("fixture did not create equivalent configuration")
	}
	if got, err := shadow.PrepareObservedWorkflowRunLink(t.Context(), row); !errors.Is(err, ErrObservation) || got.URL() != "" {
		t.Fatal("same origin but different reader adopted attempt", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := s.PrepareObservedWorkflowRunLink(canceled, row); !errors.Is(err, ErrObservation) || got.URL() != "" {
		t.Fatal("canceled caller directed a link", err)
	}
}

func TestWorkflowRunLocatorConstructsOnlyAtExtractionAndDoesNotFormatCoordinates(t *testing.T) {
	const privatePath = "https://github.com/fixture/withheld-repository/actions/runs/9007199254740993"
	n := 0
	locator := WorkflowRunLink{resolver: &workflowRunResolver{resolve: func() string {
		n++
		return privatePath
	}}}
	life, stop := context.WithCancel(t.Context())
	defer stop()
	gated := locator.WithLocalLifetime(life)
	assertOpaque := func(value any) {
		t.Helper()
		for _, style := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			if got := fmt.Sprintf(style, value); strings.Contains(got, "withheld-repository") || strings.Contains(got, "/actions/runs/") {
				t.Fatal("diagnostic exposed a run link")
			}
		}
		raw, err := json.Marshal(value)
		if err != nil || strings.Contains(string(raw), "/actions/runs/") {
			t.Fatal("JSON exposed a run link", err)
		}
	}
	if !gated.Available() {
		t.Fatal("valid lease failed")
	}
	assertOpaque(gated)
	assertOpaque(struct{ link WorkflowRunLink }{gated})
	assertOpaque([]WorkflowRunLink{gated})
	if n != 0 {
		t.Fatal("presence, diagnostics or serialization built a URL")
	}
	if got := gated.URL(); got != privatePath || n != 1 {
		t.Fatal("valid extraction failed")
	}
	stop()
	assertOpaque(struct{ link WorkflowRunLink }{gated})
	if gated.Available() || gated.URL() != "" || n != 1 {
		t.Fatal("closed reader could construct an address")
	}
}
