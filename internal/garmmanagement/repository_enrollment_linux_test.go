package garmmanagement

import (
	"context"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"sync/atomic"
	"testing"
)

type repositoryLinkFixture struct {
	*fakeRemote
	enabled                    bool
	post                       func(context.Context, RepositoryLinkPlan) (string, error)
	proof                      func(context.Context, string, RepositoryLinkPlan) (RepositoryEnrollmentProof, error)
	reviewed, posted, observed atomic.Int32
	refuse                     bool
}

func (f *repositoryLinkFixture) RepositoryEnrollmentEnabled() bool { return f.enabled }
func (f *repositoryLinkFixture) ReviewRepositoryLink(ctx context.Context, p RepositoryLinkPlan) error {
	f.reviewed.Add(1)
	if f.refuse {
		return ErrBlocked
	}
	return nil
}
func (f *repositoryLinkFixture) PostReviewedRepository(ctx context.Context, key string, p RepositoryLinkPlan) (string, error) {
	if key != p.operationKey(f.ManagementIdentity()) {
		return "", ErrJournal
	}
	f.posted.Add(1)
	return f.post(ctx, p)
}
func (f *repositoryLinkFixture) ReadSettledRepository(ctx context.Context, key, id string, p RepositoryLinkPlan) (RepositoryEnrollmentProof, error) {
	if key != p.operationKey(f.ManagementIdentity()) {
		return RepositoryEnrollmentProof{}, ErrJournal
	}
	f.observed.Add(1)
	return f.proof(ctx, id, p)
}
func linkSetup(t *testing.T) (*Manager, *Journal, *repositoryLinkFixture, RepositoryLinkPlan, string) {
	t.Helper()
	_, j, base, path := setup(t)
	f := &repositoryLinkFixture{fakeRemote: base, enabled: true}
	f.post = func(context.Context, RepositoryLinkPlan) (string, error) { return repoID, nil }
	f.proof = func(ctx context.Context, id string, p RepositoryLinkPlan) (RepositoryEnrollmentProof, error) {
		return RepositoryEnrollmentProof{p.operationKey(f.ManagementIdentity()), f.ManagementIdentity(), id, p, true}, nil
	}
	m, e := New(f, j)
	if e != nil {
		t.Fatal(e)
	}
	return m, j, f, RepositoryLinkPlan{ForgeIdentity: fingerprint, GitHubRepositoryID: 7, RecipeRevision: fingerprint, BudgetRevision: fingerprint}, path
}
func TestRepositoryEnrollmentDurableOrderingAndReplay(t *testing.T) {
	m, j, f, p, path := linkSetup(t)
	ctx := context.Background()
	f.post = func(context.Context, RepositoryLinkPlan) (string, error) {
		pending, e := j.Pending(ctx)
		if e != nil || len(pending) != 1 || len(pending[0].Result) != 0 {
			t.Fatal("intent not committed before POST", pending, e)
		}
		return repoID, nil
	}
	f.proof = func(ctx context.Context, id string, plan RepositoryLinkPlan) (RepositoryEnrollmentProof, error) {
		pending, e := j.Pending(ctx)
		if e != nil || len(pending) != 1 {
			t.Fatal("missing pending receipt")
		}
		want := RepositoryEnrollment{RequestID: p.operationKey(f.ManagementIdentity()), ManagementIdentity: f.ManagementIdentity(), JournalLineage: j.Lineage(), Plan: p}
		captured, e := decodeEnrollment(pending[0].Result, want, false)
		if e != nil || captured.RepositoryID != repoID {
			t.Fatal("uncommitted receipt before readback", e)
		}
		return RepositoryEnrollmentProof{plan.operationKey(f.ManagementIdentity()), f.ManagementIdentity(), id, plan, true}, nil
	}
	enrolled, e := m.EnrollReviewedRepository(ctx, p)
	if e != nil || !enrolled.Confirmed || enrolled.RepositoryID != repoID {
		t.Fatal(enrolled, e)
	}
	j2, e := OpenJournal(path, j.Lineage())
	if e != nil {
		t.Fatal(e)
	}
	defer j2.Close()
	m2, e := New(f, j2)
	if e != nil {
		t.Fatal(e)
	}
	replay, e := m2.EnrollReviewedRepository(ctx, p)
	if e != nil || replay != enrolled || f.reviewed.Load() != 1 || f.posted.Load() != 1 || f.observed.Load() != 1 {
		t.Fatal(replay, e)
	}
	changed := p
	changed.BudgetRevision = f.ManagementIdentity()
	if _, e = m2.EnrollReviewedRepository(ctx, changed); !errors.Is(e, ErrRequestConflict) {
		t.Fatal(e)
	}
}
func TestRepositoryEnrollmentLostACKNeverCreatesOrUsesListing(t *testing.T) {
	m, _, f, p, _ := linkSetup(t)
	ctx := context.Background()
	f.post = func(context.Context, RepositoryLinkPlan) (string, error) { return "", errors.New("EOF") }
	if _, e := m.EnrollReviewedRepository(ctx, p); !errors.Is(e, ErrOutcomeUnknown) {
		t.Fatal(e)
	}
	if _, e := m.EnrollReviewedRepository(ctx, p); !errors.Is(e, ErrBlocked) {
		t.Fatal(e)
	}
	if _, e := m.RecoverReviewedRepository(ctx, p); !errors.Is(e, ErrBlocked) {
		t.Fatal(e)
	}
	if _, e := m.ReconcilePending(ctx); !errors.Is(e, ErrBlocked) {
		t.Fatal(e)
	}
	if f.reads != 0 || f.posted.Load() != 1 || f.observed.Load() != 0 {
		t.Fatal("replayed POST or guessed a GARM UUID")
	}
}
func TestRepositoryEnrollmentExactReadbackRecovery(t *testing.T) {
	m, j, f, p, _ := linkSetup(t)
	ctx := context.Background()
	verify := f.proof
	f.proof = func(_ context.Context, id string, p RepositoryLinkPlan) (RepositoryEnrollmentProof, error) {
		p.GitHubRepositoryID++
		return RepositoryEnrollmentProof{p.operationKey(f.ManagementIdentity()), f.ManagementIdentity(), id, p, true}, nil
	}
	if _, e := m.EnrollReviewedRepository(ctx, p); !errors.Is(e, ErrOutcomeUnknown) {
		t.Fatal(e)
	}
	if _, e := m.RecoverReviewedRepository(ctx, p); !errors.Is(e, ErrOutcomeUnknown) {
		t.Fatal(e)
	}
	bad := p
	bad.GitHubRepositoryID = 8
	if _, e := m.EnrollReviewedRepository(ctx, bad); !errors.Is(e, ErrBlocked) {
		t.Fatal(e)
	}
	f.proof = verify
	out, e := m.RecoverReviewedRepository(ctx, p)
	if e != nil || !out.Confirmed || out.RepositoryID != repoID {
		t.Fatal(out, e)
	}
	rows, e := j.Pending(ctx)
	if e != nil || len(rows) != 0 || f.posted.Load() != 1 {
		t.Fatal("recovery redispatched or failed to settle", rows, e)
	}
}
func TestRepositoryEnrollmentDefaultsOffAndRefusesStaleReview(t *testing.T) {
	m, j, f, p, _ := linkSetup(t)
	ctx := context.Background()
	plain, e := New(f.fakeRemote, j)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = plain.EnrollReviewedRepository(ctx, p); !errors.Is(e, garmapi.ErrManagementDisabled) {
		t.Fatal(e)
	}
	f.enabled = false
	if _, e = m.EnrollReviewedRepository(ctx, p); !errors.Is(e, garmapi.ErrManagementDisabled) {
		t.Fatal(e)
	}
	f.enabled = true
	bad := p
	bad.GitHubRepositoryID = 0
	if _, e = m.EnrollReviewedRepository(ctx, bad); !errors.Is(e, garmapi.ErrManagementUnsafe) {
		t.Fatal(e)
	}
	f.refuse = true
	if _, e = m.EnrollReviewedRepository(ctx, p); !errors.Is(e, garmapi.ErrManagementConflict) {
		t.Fatal(e)
	}
	rows, e := j.Pending(ctx)
	if e != nil || len(rows) != 0 || f.posted.Load() != 0 {
		t.Fatal(rows, e)
	}
}

func TestRepositoryEnrollmentTwoManagersOneDispatch(t *testing.T) {
	m, j, f, p, path := linkSetup(t)
	ctx := context.Background()
	j2, e := OpenJournal(path, j.Lineage())
	if e != nil {
		t.Fatal(e)
	}
	defer j2.Close()
	other, e := New(f, j2)
	if e != nil {
		t.Fatal(e)
	}
	pending := make(chan struct{})
	release := make(chan struct{})
	closed := false
	defer func() {
		if !closed {
			close(release)
		}
	}()
	f.post = func(ctx context.Context, p RepositoryLinkPlan) (string, error) {
		close(pending)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-release:
			return repoID, nil
		}
	}
	done := make(chan error, 1)
	go func() { _, e := m.EnrollReviewedRepository(ctx, p); done <- e }()
	<-pending
	if _, e = other.EnrollReviewedRepository(ctx, p); !errors.Is(e, ErrBlocked) {
		t.Fatal(e)
	}
	if _, e = other.RecoverReviewedRepository(ctx, p); !errors.Is(e, ErrBlocked) {
		t.Fatal(e)
	}
	close(release)
	closed = true
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if _, e = other.EnrollReviewedRepository(ctx, p); e != nil || f.posted.Load() != 1 {
		t.Fatal(e, f.posted.Load())
	}
}
