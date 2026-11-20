package demandrepair

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCIRateDirective(t *testing.T) {
	now := time.Unix(1_800_000_000, 500_000_000)
	epoch := func(after time.Duration) string { return strconv.FormatInt(now.Add(after).Unix(), 10) }
	for _, tc := range []struct {
		name                    string
		code                    int
		retry, remaining, reset []string
		wait                    time.Duration
		block, refused          bool
	}{
		{name: "ordinary with unrelated reset", code: 200, remaining: []string{"25"}, reset: []string{"unused"}},
		{name: "status only", code: 429, block: true, wait: time.Minute},
		{name: "forbidden without evidence", code: 403, block: true, wait: time.Minute},
		{name: "successful exhausted quota", code: 200, remaining: []string{"0"}, reset: []string{epoch(10 * time.Minute)}, block: true, wait: 10*time.Minute - 500*time.Millisecond},
		{name: "primary reset later", code: 403, retry: []string{"60", "90"}, remaining: []string{"00"}, reset: []string{epoch(20 * time.Minute), epoch(30 * time.Minute)}, block: true, wait: 30*time.Minute - 500*time.Millisecond},
		{name: "retry after later", code: 429, retry: []string{"3600"}, remaining: []string{"0"}, reset: []string{epoch(10 * time.Minute)}, block: true, wait: time.Hour},
		{name: "no reset at all", code: 403, retry: []string{"60"}, remaining: []string{"0"}, block: true, wait: time.Hour},
		{name: "present empty", code: 200, retry: []string{""}, block: true, refused: true},
		{name: "explicit date unsupported", code: 429, retry: []string{"Wed, 21 Oct 2015 07:28:00 GMT"}, block: true, refused: true},
		{name: "past reset", code: 403, remaining: []string{"0"}, reset: []string{epoch(-time.Minute)}, block: true, refused: true},
		{name: "ambiguous reset cannot shorten", code: 403, retry: []string{"3600"}, remaining: []string{"0"}, reset: []string{epoch(10 * time.Minute), "..."}, block: true, refused: true},
		{name: "long reset not clamped", code: 403, remaining: []string{"0"}, reset: []string{epoch(25 * time.Hour)}, block: true, refused: true},
		{name: "overflow not clamped", code: 429, retry: []string{"18446744073709551616"}, block: true, refused: true},
		{name: "unreadable remaining with no reset", code: 200, remaining: []string{"invalid"}, block: true, wait: time.Hour},
		{name: "remaining conflicted still blocks", code: 200, remaining: []string{"0", "25"}, reset: []string{epoch(30 * time.Minute)}, block: true, wait: 30*time.Minute - 500*time.Millisecond},
		{name: "excess header cardinality", code: 429, retry: make([]string, ciRateMaxHeaderValues+1), block: true, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for name, vv := range map[string][]string{"Retry-After": tc.retry, "X-RateLimit-Remaining": tc.remaining, "X-RateLimit-Reset": tc.reset} {
				for _, v := range vv {
					h.Add(name, v)
				}
			}
			if wait, block, refused := ciRateDirective(now, tc.code, h); wait != tc.wait || block != tc.block || refused != tc.refused {
				t.Fatalf("gate result: %v, %t, %t", wait, block, refused)
			}
		})
	}
}

func TestCIRateGateTLSRefusesBeforeCredentialsAndLeavesOrdinaryReaderAlone(t *testing.T) {
	var now, resolves, requests atomic.Int64
	const start = int64(1_800_000_000)
	now.Store(start * int64(time.Second))
	first, proceed := make(chan struct{}), make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/example/project" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("invalid scoped read")
		}
		switch requests.Add(1) {
		case 1:
			close(first)
			<-proceed
			w.Header().Set("Retry-After", "480")
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(start+720, 10))
			w.WriteHeader(http.StatusTooManyRequests)
		case 4:
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", "invalid")
		}
		_, _ = w.Write([]byte(`{"id":1,"full_name":"example/project"}`))
	}))
	defer server.Close()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(proceed) }) }
	defer release()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	repos := []Repository{{GarmID: "fixture", Host: "github.example", Owner: "example", Name: "project"}}
	resolve := func(context.Context, string) (string, error) { resolves.Add(1); return "fixture", nil }
	input := func(dedicated bool) (*GitHubClient, error) {
		if dedicated {
			return NewCIHealthGitHub(server.URL, "github.example", "fixture", resolve, &tls.Config{RootCAs: roots}, repos)
		}
		return NewGitHub(server.URL, "github.example", "fixture", resolve, &tls.Config{RootCAs: roots}, repos)
	}
	dedicated, err := input(true)
	if err != nil {
		t.Fatal(err)
	}
	defer dedicated.Close()
	dedicated.ciRate.clock = func() time.Time { return time.Unix(0, now.Load()) }
	ordinary, err := input(false)
	if err != nil {
		t.Fatal(err)
	}
	defer ordinary.Close()
	get := func(c *GitHubClient) error {
		var out map[string]any
		return c.get(t.Context(), "/repos/example/project", nil, &out)
	}
	done := make(chan error, 1)
	go func() { done <- get(dedicated) }()
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("first request not sent")
	}
	if err := get(dedicated); !errors.Is(err, ErrObservation) || resolves.Load() != 1 || requests.Load() != 1 {
		t.Fatal("second local contender resolved credentials or reached HTTP", err)
	}
	release()
	if err := <-done; err != ErrObservation {
		t.Fatal("rate-limited response returned success", err)
	}
	// Five minute worker cadence, forward just before cutoff, and a
	// rollback after that sample all refuse before credential resolution.
	for _, offset := range []time.Duration{5 * time.Minute, 12*time.Minute - 1, 9 * time.Minute} {
		now.Store(start*int64(time.Second) + int64(offset))
		if err := get(dedicated); err != ErrObservation || resolves.Load() != 1 || requests.Load() != 1 {
			t.Fatal("blocked or backward clock resolved credentials/reached HTTP", err)
		}
	}
	if err := get(ordinary); err != nil || resolves.Load() != 2 || requests.Load() != 2 {
		t.Fatal("CI gate leaked to ordinary client", err)
	}
	now.Store((start + 720) * int64(time.Second))
	if err := get(dedicated); err != nil || resolves.Load() != 3 || requests.Load() != 3 {
		t.Fatal("exact latest cutoff did not permit existing read", err)
	}
	if err := get(dedicated); err != ErrObservation || requests.Load() != 4 {
		t.Fatal("malformed quota response accepted", err)
	}
	now.Add(48 * int64(time.Hour))
	if err := get(dedicated); err != ErrObservation || resolves.Load() != 4 || requests.Load() != 4 {
		t.Fatal("guess shortened an unusable explicit reset", err)
	}

	// A different dedicated transport starts with its own local state. A
	// rollback while resolving its credential must still block dispatch.
	late, err := NewCIHealthGitHub(server.URL, "github.example", "fixture", func(context.Context, string) (string, error) {
		resolves.Add(1)
		now.Add(-int64(time.Second))
		return "fixture", nil
	}, &tls.Config{RootCAs: roots}, repos)
	if err != nil {
		t.Fatal(err)
	}
	defer late.Close()
	late.ciRate.clock = func() time.Time { return time.Unix(0, now.Load()) }
	if err := get(late); err != ErrObservation || resolves.Load() != 5 || requests.Load() != 4 {
		t.Fatal("clock rollback during credential resolution reached HTTP", err)
	}
}
