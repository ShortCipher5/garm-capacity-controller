//go:build linux

package nativeprovision

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprepare"
	"golang.org/x/crypto/ssh"
	"os"
	"testing"
)

func attemptFixture(t *testing.T) *Service {
	b, target := fixture(t)
	p, _ := b.Plan(target)
	return &Service{bundle: b, plan: p, dir: t.TempDir(), uid: uint32(os.Geteuid()), execute: func(context.Context, Plan, string, ssh.Signer, bool) (nativeprepare.Receipt, error) {
		return testReceipt(p.Manifest), nil
	}}
}
func TestDistinctInspectionResultDoesNotReuseHistoricalReceipt(t *testing.T) {
	s := attemptFixture(t)
	resolve := func() (ssh.Signer, error) { return nil, nil }
	state, e := s.Apply(context.Background(), s.ReviewDigest(), false, resolve)
	if e != nil || state.Attempt == nil || state.Attempt.Sequence != 1 || state.Attempt.Mode != "prepare" || state.Attempt.Status != "prepared_not_activated" {
		t.Fatal(state, e)
	}
	s.execute = func(context.Context, Plan, string, ssh.Signer, bool) (nativeprepare.Receipt, error) {
		return nativeprepare.Receipt{}, ErrUnknown
	}
	state, e = s.Apply(context.Background(), s.ReviewDigest(), true, resolve)
	if e != ErrUnknown || state.Attempt.Sequence != 2 || state.Attempt.Mode != "inspect" || state.Attempt.Status != "observation_unavailable" {
		t.Fatal(state, e)
	}
	got, e := s.Status()
	if e != nil || got.Status != "prepared_not_activated" || got.Receipt == nil || got.Attempt.Sequence != 2 || got.Attempt.Status != "observation_unavailable" || got.Attempt.Receipt != nil {
		t.Fatal("historical receipt must not mask new failed inspect", got, e)
	}
	s.execute = func(context.Context, Plan, string, ssh.Signer, bool) (nativeprepare.Receipt, error) {
		return testReceipt(s.plan.Manifest), nil
	}
	state, e = s.Apply(context.Background(), s.ReviewDigest(), true, resolve)
	if e != nil || state.Attempt.Sequence != 3 || state.Attempt.Receipt == nil {
		t.Fatal(state, e)
	}
	restarted := &Service{plan: s.plan, dir: s.dir, uid: s.uid}
	got, e = restarted.Status()
	if e != nil || got.Attempt.Sequence != 3 || got.Attempt.Status != "prepared_not_activated" || got.Attempt.CompletedAt == nil {
		t.Fatal(got, e)
	}
}
func TestAttemptUnknownIsSyncedBeforeResolver(t *testing.T) {
	s := attemptFixture(t)
	calls := 0
	_, e := s.Apply(context.Background(), s.ReviewDigest(), false, func() (ssh.Signer, error) {
		calls++
		a, e := s.latestAttempt()
		if e != nil || a == nil || a.Status != "unknown" || a.Sequence != 1 || a.CompletedAt != nil {
			t.Fatal(a, e)
		}
		return nil, ErrInvalid
	})
	if e != ErrUnknown || calls != 1 {
		t.Fatal(e, calls)
	}
}
func TestInterruptedAttemptAndMalformedResultFailClosed(t *testing.T) {
	s := attemptFixture(t)
	initial := State{ReviewDigest: s.ReviewDigest(), Status: "unknown"}
	raw, _ := json.Marshal(initial)
	if e := s.write(s.plan.Manifest.ID+".intent.json", raw); e != nil {
		t.Fatal(e)
	}
	a, e := s.beginAttempt(true)
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.Status()
	if e != nil || got.Attempt.Sequence != a.Sequence || got.Attempt.Status != "unknown" {
		t.Fatal(got, e)
	}
	a.ReviewDigest = "other"
	raw, _ = json.Marshal(a)
	if s.write(s.attemptName(a.Sequence, "result"), raw) != nil {
		t.Fatal("write")
	}
	if _, e = s.Status(); e == nil {
		t.Fatal("mismatched result accepted")
	}
}
func TestAttemptLimitPreventsAdditionalEffects(t *testing.T) {
	s := attemptFixture(t)
	for seq := uint64(1); seq <= maxAttempts; seq++ {
		if s.write(s.attemptName(seq, "intent"), []byte("{}")) != nil {
			t.Fatal("write")
		}
	}
	if _, e := s.beginAttempt(true); e == nil {
		t.Fatal("unbounded attempt log")
	}
}
func TestOrphanAttemptCannotPermitFreshUpload(t *testing.T) {
	s := attemptFixture(t)
	if _, e := s.beginAttempt(true); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Status(); e == nil {
		t.Fatal("orphan hidden")
	}
	_, e := s.Apply(context.Background(), s.ReviewDigest(), false, func() (ssh.Signer, error) { t.Fatal("resolved before orphan refusal"); return nil, nil })
	if e != ErrInvalid {
		t.Fatal(e)
	}
}
