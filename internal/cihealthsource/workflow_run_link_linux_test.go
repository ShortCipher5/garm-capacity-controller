package cihealthsource

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

func TestObservedWorkflowRunLinkUsesExactLiveCycleWithoutRenewingAttempt(t *testing.T) {
	// Only generates a fixture trust root. Neither of the exact GitHub.com
	// configured readers below makes a request here.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("pure projection made a request")
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	creds := t.TempDir()
	token, ca := filepath.Join(creds, "token"), filepath.Join(creds, "ca")
	if err := os.Chmod(creds, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("unused-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	repo := demandrepair.Repository{GarmID: "fixture", Host: "github.com", Owner: "unlogged-fixture", Name: "unlogged-repository"}
	cfg := demandobserver.Config{Enabled: true, GitHubEndpoint: "https://api.github.com", GitHubAuthority: repo.Host,
		GitHubTokenFile: token, GitHubCAFile: ca, Repositories: []demandrepair.Repository{repo},
		CheckpointPath: filepath.Join(creds, "unused"), InitialFrom: time.Now().Add(-time.Hour),
		IntervalSeconds: 60, MaxBackoffSeconds: 60}
	input := demandobserver.CIHealthInput{Enabled: true, Repositories: []demandobserver.CIHealthRepositoryPin{{GarmID: repo.GarmID, GitHubID: repoID}}}
	newOwner := func() *demandobserver.CIHealthSource {
		s, err := demandobserver.NewCIHealthSource(cfg, input)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		return s
	}
	owner, shadow := newOwner(), newOwner()
	client, err := demandrepair.NewGitHub(cfg.GitHubEndpoint, repo.Host, "unused", func(context.Context, string) (string, error) {
		t.Error("pure projection requested credentials")
		return "", demandrepair.ErrObservation
	}, &tls.Config{RootCAs: x509.NewCertPool()}, []demandrepair.Repository{repo})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	linkSource, err := demandrepair.NewCIHealthSource(client, []demandrepair.CIRepository{{Repository: repo, GitHubID: repoID}})
	if err != nil {
		t.Fatal(err)
	}
	key := cihealth.AttemptKey{Source: owner.SourceID(), RepositoryID: repoID, WorkflowID: 7, RunID: math.MaxInt64, Attempt: 2}
	locator, err := linkSource.WorkflowRunLink(key.Source, key.RepositoryID, key.RunID)
	if err != nil || owner.SourceID() != shadow.SourceID() {
		t.Fatal("identity fixture failed", err)
	}
	// This private cycle fixture avoids contacting GitHub. The reader is real;
	// the exact attempt-mint check has an independent source-package test.
	sealed := time.Now()
	c := Cycle{state: CyclePartial, source: captureCycleSource(owner), sealedAt: sealed, observedAt: sealed.UTC(),
		facts:    []cihealth.AttemptFacts{{Key: key, AsOf: sealed.UTC(), Clock: cihealth.Clock{ObservedAt: sealed.UTC().Add(-time.Second)}}},
		runLinks: map[cihealth.AttemptKey]demandrepair.WorkflowRunLink{key: locator},
	}
	link, ok := c.ObservedWorkflowRunLink(owner, key)
	if href, active := link.URL(); !ok || !active || href != "https://github.com/unlogged-fixture/unlogged-repository/actions/runs/9223372036854775807" {
		t.Fatal("positive item in partial cycle did not project logical run")
	}
	assertRefused := func(c Cycle, s *demandobserver.CIHealthSource, k cihealth.AttemptKey) {
		t.Helper()
		if got, ok := c.ObservedWorkflowRunLink(s, k); ok || got != (ObservedWorkflowRunLink{}) {
			t.Fatal("unproved observation yielded a link")
		}
	}
	assertRefused(c, shadow, key)
	assertRefused(c, nil, key)
	assertRefused(Cycle{}, owner, key)
	for _, bad := range []cihealth.AttemptKey{
		{Source: "unscoped", RepositoryID: repoID, WorkflowID: 7, RunID: math.MaxInt64, Attempt: 2},
		{Source: key.Source, RepositoryID: repoID + 1, WorkflowID: 7, RunID: math.MaxInt64, Attempt: 2},
		{Source: key.Source, RepositoryID: repoID, WorkflowID: 8, RunID: math.MaxInt64, Attempt: 2},
		{Source: key.Source, RepositoryID: repoID, WorkflowID: 7, RunID: math.MaxInt64 - 1, Attempt: 2},
		{Source: key.Source, RepositoryID: repoID, WorkflowID: 7, RunID: math.MaxInt64, Attempt: 1},
	} {
		assertRefused(c, owner, bad)
	}
	absent := c
	absent.facts = nil
	assertRefused(absent, owner, key)
	stale := c
	stale.facts = c.Facts()
	stale.facts[0].Clock.ObservedAt = sealed.UTC().Add(-historyCycleFreshness)
	assertRefused(stale, owner, key)
	plain := c
	plain.source = captureCycleSource(linkSource)
	assertRefused(plain, owner, key)
	noMono := c
	noMono.sealedAt = sealed.Round(0)
	assertRefused(noMono, owner, key)
	diagnostics := func(l ObservedWorkflowRunLink) {
		t.Helper()
		for _, item := range []any{c, &c, l, &l, Observation{runLink: locator}, struct {
			cycle       Cycle
			link        ObservedWorkflowRunLink
			observation Observation
		}{c, l, Observation{runLink: locator}}} {
			for _, style := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
				got := fmt.Sprintf(style, item)
				if strings.Contains(got, "unlogged-fixture") || strings.Contains(got, "unlogged-repository") || strings.Contains(got, "/actions/runs/") {
					t.Fatal("formatter disclosed link coordinates")
				}
			}
			raw, err := json.Marshal(item)
			if err != nil || strings.Contains(string(raw), "unlogged-fixture") || strings.Contains(string(raw), "unlogged-repository") || strings.Contains(string(raw), "/actions/runs/") {
				t.Fatal("JSON disclosed link coordinates", err)
			}
		}
		wrapped := fmt.Errorf("unavailable: %v", struct{ link ObservedWorkflowRunLink }{l})
		if strings.Contains(wrapped.Error(), "unlogged-repository") || strings.Contains(wrapped.Error(), "/actions/runs/") {
			t.Fatal("error disclosed link coordinates")
		}
	}
	diagnostics(link)
	fence := *link.resolver
	fence.proof.deadline = sealed.Add(-time.Nanosecond)
	staleProof := ObservedWorkflowRunLink{resolver: &fence}
	if href, ok := staleProof.URL(); ok || href != "" {
		t.Fatal("expired handle exposed address")
	}
	diagnostics(staleProof)
	owner.Close()
	diagnostics(link)
	assertRefused(c, owner, key)
	if href, ok := link.URL(); ok || href != "" {
		t.Fatal("previous handle escaped reader shutdown")
	}
}
