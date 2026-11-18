package managedgarm

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestUnmintedSessionCannotDispatch(t *testing.T) {
	s := &Session{}
	for _, path := range []string{"/api/v1/pools/11111111-1111-4111-8111-111111111111", "/api/v1/auth/login", "/api/v1/first-run"} {
		r, _ := http.NewRequest("GET", "https://managed-garm.invalid"+path, nil)
		if _, e := s.RoundTrip(r); e == nil {
			t.Fatal("unminted session accepted")
		}
	}
	if s.Identity() != "" || s.ControllerID() != "" {
		t.Fatal("unminted session asserted identity")
	}
	if _, e := s.ReadCollection(context.Background(), "/api/v1/jobs"); e == nil {
		t.Fatal("unminted collection read")
	}
}
func TestSessionRefusesCapacityIncreasesWithoutNetwork(t *testing.T) {
	s := &Session{token: "internal-test-token"}
	for _, body := range []string{`{"max_runners":1000001}`, `{"enabled":true}`, `{"min_idle_runners":0}`, `{"tags":["a"]}`, `{"unknown":false}`} {
		r, _ := http.NewRequest("PUT", "https://managed-garm.invalid/api/v1/pools/11111111-1111-4111-8111-111111111111", strings.NewReader(body))
		if _, e := s.RoundTrip(r); e == nil {
			t.Fatal("unsafe writer accepted", body)
		}
	}
	// No valid transport observation was available or requested by these refusals.
	if s.revoked {
		t.Fatal("unsafe request reached topology/network")
	}
	s.Close()
	if s.token != "" || !s.revoked {
		t.Fatal("close retained authority")
	}
}

func TestSupervisorFilePathRequiresCanonicalProtectedAncestors(t *testing.T) {
	for _, p := range []string{"relative", "/", "/tmp/../tmp/file", "/tmp/file"} {
		if ValidateSupervisorFilePath(p) == nil {
			t.Fatal("unsafe root input", p)
		}
	}
}
