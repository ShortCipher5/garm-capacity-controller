package admission

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func connectionFixture() Connection {
	return Connection{ID: "new-host", CredentialRef: "fixture", Endpoint: "https://192.0.2.10:8443", Project: "runners", ServerKeySHA256: strings.Repeat("a", 64)}
}
func TestConnectionLifecycleCASAndReviewInvalidation(t *testing.T) {
	s := detectFixture(t)
	ctx := context.Background()
	c, err := s.PutConnection(ctx, connectionFixture(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.State != "draft" || c.Revision != 1 {
		t.Fatal(c)
	}
	if _, err = s.PutConnection(ctx, connectionFixture(), 0); !errors.Is(err, ErrConflict) {
		t.Fatal("stale draft accepted")
	}
	if _, err = s.TransitionConnection(ctx, c.ID, 1, "approved", strings.Repeat("b", 64)); !errors.Is(err, ErrDenied) {
		t.Fatal("unreviewed key approved")
	}
	c, err = s.TransitionConnection(ctx, c.ID, 1, "approved", c.ServerKeySHA256)
	if err != nil {
		t.Fatal(err)
	}
	review, err := s.RecordConnectionDiscoveryReview(ctx, "11111111-1111-4111-8111-111111111111", c.ID, time.Now().UTC(), []byte(`{}`), nil, c.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordDiscoveryReview(ctx, "22222222-2222-4222-8222-222222222222", c.ID, time.Now().UTC(), []byte(`{}`), nil); !errors.Is(err, ErrConflict) {
		t.Fatal("unversioned dynamic review accepted")
	}
	old := c.Revision
	c, err = s.TransitionConnection(ctx, c.ID, c.Revision, "revoked", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnrollHostFromReview(ctx, "host-b", Resources{CPU: 1, RAM: 1}, "disabled", review); !errors.Is(err, ErrConflict) {
		t.Fatal("revoked review enrolled host")
	}
	if _, err = s.RecordConnectionDiscoveryReview(ctx, "33333333-3333-4333-8333-333333333333", c.ID, time.Now().UTC(), []byte(`{}`), nil, old); !errors.Is(err, ErrConflict) {
		t.Fatal("late discovery republished after revocation")
	}
	c, err = s.PutConnection(ctx, connectionFixture(), c.Revision)
	if err != nil || c.State != "draft" {
		t.Fatal(c, err)
	}
	reopened, err := OpenSQLite(s.path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	stored, err := reopened.Connection(ctx, c.ID)
	if err != nil || stored != c {
		t.Fatal("connection not durable", stored, err)
	}
}
func TestConnectionEndpointCannotSmuggleDestination(t *testing.T) {
	for _, endpoint := range []string{"https://192.0.2.10:8443", "https://[2001:db8::1]:8443"} {
		if !ValidConnectionEndpoint(endpoint) {
			t.Fatal(endpoint)
		}
	}
	for _, endpoint := range []string{"http://192.0.2.10:8443", "https://host.invalid:443", "https://192.0.2.10", "https://192.0.2.10:443/", "https://user@192.0.2.10:443", "https://192.0.2.10:0443", "https://[fe80::1%25eth0]:443", "https://0.0.0.0:443", "https://192.0.2.10:443?project=evil", "https://192.0.2.10:443#x"} {
		if ValidConnectionEndpoint(endpoint) {
			t.Fatal("unsafe endpoint", endpoint)
		}
	}
}
func TestConnectionAuditFailureRollsBack(t *testing.T) {
	s := detectFixture(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`CREATE TRIGGER fail_connection BEFORE INSERT ON audit WHEN NEW.kind='onboarding_connection_changed' BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutConnection(ctx, connectionFixture(), 0); err == nil {
		t.Fatal("missing audit accepted")
	}
	if _, err := s.Connection(ctx, "new-host"); !errors.Is(err, ErrDenied) {
		t.Fatal("failed write persisted")
	}
}
