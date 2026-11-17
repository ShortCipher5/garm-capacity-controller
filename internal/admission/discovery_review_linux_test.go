package admission

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDiscoveryReviewEnrollmentIsDurableSingleUseAndUnhealthy(t *testing.T) {
	s := detectFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	totals := Resources{CPU: 8000, RAM: 32000, Disk: 200000}
	r, err := s.RecordDiscoveryReview(ctx, "11111111-1111-4111-8111-111111111111", "profile-a", now, []byte(`{"totals":"fixture"}`), &totals)
	if err != nil {
		t.Fatal(err)
	}
	budget := Resources{CPU: 1000, RAM: 2000, Disk: 20000, Runners: 1}
	tampered := r
	tampered.Revision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if !errors.Is(s.EnrollHostFromReview(ctx, "host-b", budget, "disabled", tampered), ErrConflict) {
		t.Fatal("tampered review accepted")
	}
	if err = s.EnrollHostFromReview(ctx, "host-b", budget, "disabled", r); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	host := snap.Config.Hosts["host-b"]
	if host.Healthy || host.Mode != "disabled" || host.Detected == nil || *host.Detected != totals || !host.ObservedAt.Equal(now) {
		t.Fatalf("unsafe enrollment: %+v", host)
	}
	if !errors.Is(s.EnrollHostFromReview(ctx, "host-c", budget, "disabled", r), ErrConflict) {
		t.Fatal("review reused")
	}
	if auditKinds(snap)["onboarding_review_consumed"] != 1 {
		t.Fatal("missing consumption audit")
	}
	// A new store handle cannot reset consumption.
	reopened, err := OpenSQLite(s.path, snap.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !errors.Is(reopened.EnrollHostFromReview(ctx, "host-c", budget, "disabled", r), ErrConflict) {
		t.Fatal("restart reset consumption")
	}
}

func TestDiscoveryReviewRejectsStaleSupersededAndFuture(t *testing.T) {
	s := detectFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	budget := Resources{CPU: 1, RAM: 1}
	record := func(id string) DiscoveryReview {
		t.Helper()
		r, err := s.RecordDiscoveryReview(ctx, id, "profile-a", now, []byte(`{}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	first := record("11111111-1111-4111-8111-111111111111")
	now = now.Add(time.Second)
	second := record("22222222-2222-4222-8222-222222222222")
	if !errors.Is(s.EnrollHostFromReview(ctx, "host-b", budget, "disabled", first), ErrConflict) {
		t.Fatal("superseded review accepted")
	}
	now = now.Add(2 * time.Minute)
	if !errors.Is(s.EnrollHostFromReview(ctx, "host-b", budget, "disabled", second), ErrConflict) {
		t.Fatal("stale review accepted")
	}
	now = now.Add(-3 * time.Minute)
	if !errors.Is(s.EnrollHostFromReview(ctx, "host-b", budget, "disabled", second), ErrConflict) {
		t.Fatal("future review accepted")
	}
	snap, _ := s.Snapshot(ctx)
	if len(snap.Config.Hosts) != 1 {
		t.Fatal("rejected review enrolled host")
	}
}

func TestDiscoveryReviewConflictDoesNotConsume(t *testing.T) {
	s := detectFixture(t)
	ctx := context.Background()
	r, err := s.RecordDiscoveryReview(ctx, "11111111-1111-4111-8111-111111111111", "profile-a", time.Now().UTC(), []byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	budget := Resources{CPU: 1, RAM: 1}
	if !errors.Is(s.EnrollHostFromReview(ctx, "host-a", budget, "disabled", r), ErrConflict) {
		t.Fatal("existing host accepted")
	}
	if err = s.EnrollHostFromReview(ctx, "host-b", budget, "disabled", r); err != nil {
		t.Fatal("failed transaction consumed review", err)
	}
}

func TestDiscoveryReviewConcurrentConsumption(t *testing.T) {
	s := detectFixture(t)
	ctx := context.Background()
	r, err := s.RecordDiscoveryReview(ctx, "11111111-1111-4111-8111-111111111111", "profile-a", time.Now().UTC(), []byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, name := range []string{"host-b", "host-c"} {
		go func(name string) {
			results <- s.EnrollHostFromReview(ctx, name, Resources{CPU: 1, RAM: 1}, "disabled", r)
		}(name)
	}
	successes := 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful consumers=%d", successes)
	}
}

func TestOlderDiscoveryCannotSupersedeNewer(t *testing.T) {
	s := detectFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	r, err := s.RecordDiscoveryReview(ctx, "11111111-1111-4111-8111-111111111111", "profile-a", now, []byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordDiscoveryReview(ctx, "22222222-2222-4222-8222-222222222222", "profile-a", now.Add(-time.Second), []byte(`{}`), nil); !errors.Is(err, ErrConflict) {
		t.Fatal("out-of-order completion superseded newer review", err)
	}
	if err = s.EnrollHostFromReview(ctx, "host-b", Resources{CPU: 1, RAM: 1}, "disabled", r); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryReviewAuditFailureRollsBackConsumptionAndHost(t *testing.T) {
	s := detectFixture(t)
	ctx := context.Background()
	r, err := s.RecordDiscoveryReview(ctx, "11111111-1111-4111-8111-111111111111", "profile-a", time.Now().UTC(), []byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER fail_review_audit BEFORE INSERT ON audit WHEN NEW.kind='onboarding_review_consumed' BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	budget := Resources{CPU: 1, RAM: 1}
	if s.EnrollHostFromReview(ctx, "host-b", budget, "disabled", r) == nil {
		t.Fatal("audit failure accepted")
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap.Config.Hosts["host-b"]; ok {
		t.Fatal("host escaped transaction rollback")
	}
	if _, err = s.db.Exec("DROP TRIGGER fail_review_audit"); err != nil {
		t.Fatal(err)
	}
	if err = s.EnrollHostFromReview(ctx, "host-b", budget, "disabled", r); err != nil {
		t.Fatal("rolled-back consumption persisted", err)
	}
}
