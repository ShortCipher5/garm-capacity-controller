//go:build linux

package nativeprovision

import (
	"context"
	"golang.org/x/crypto/ssh"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func replacementFixture(t *testing.T, old *Service) *Service {
	t.Helper()
	b, _ := fixture(t)
	// The transport is not invoked in these registry tests; use a distinct reviewed
	// plan to exercise exact journal matching rather than a fake physical receipt.
	p, _ := b.Plan(old.plan.Target)
	p.ScriptSHA256 = strings.Repeat("b", 64)
	return &Service{bundle: b, plan: p, dir: old.dir, uid: old.uid, execute: old.execute}
}
func TestRegisteredReplacementFencesOldPlanAcrossRestart(t *testing.T) {
	old := attemptFixture(t)
	next := replacementFixture(t, old)
	r, e := next.RegisterReviewedReplacement(context.Background(), old, strings.Repeat("c", 64))
	if e != nil || r.Sequence != 1 {
		t.Fatal(r, e)
	}
	if _, e = old.Status(); e == nil {
		t.Fatal("old review remained usable")
	}
	calls := 0
	if _, e = old.Apply(context.Background(), old.ReviewDigest(), false, func() (ssh.Signer, error) { calls++; return nil, ErrInvalid }); e == nil || calls != 0 {
		t.Fatal("stale plan contacted credentials")
	}
	restarted := &Service{plan: next.plan, dir: next.dir, uid: next.uid}
	if state, e := restarted.Status(); e != nil || state.Status != "not_started" {
		t.Fatal(state, e)
	}
	again, e := next.RegisterReviewedReplacement(context.Background(), old, strings.Repeat("c", 64))
	if e != nil || again != r {
		t.Fatal(again, e)
	}
}
func TestRegisteredReplacementRejectsConsumedMalformedAndTargetDrift(t *testing.T) {
	for _, mode := range []string{"intent", "receipt", "attempt", "gap", "target", "locked"} {
		t.Run(mode, func(t *testing.T) {
			old := attemptFixture(t)
			next := replacementFixture(t, old)
			var closeLock func()
			switch mode {
			case "intent", "receipt":
				os.WriteFile(filepath.Join(old.dir, old.plan.Manifest.ID+"."+mode+".json"), []byte("{}"), 0600)
			case "attempt":
				os.WriteFile(filepath.Join(old.dir, old.plan.Manifest.ID+".attempt.000001.intent.json"), []byte("{}"), 0600)
			case "gap":
				os.WriteFile(filepath.Join(old.dir, old.plan.Manifest.ID+".registered.000002.json"), []byte("{}"), 0600)
			case "target":
				next.plan.Target.Port++
			case "locked":
				f, e := old.lock()
				if e != nil {
					t.Fatal(e)
				}
				closeLock = func() { f.Close() }
			}
			if closeLock != nil {
				defer closeLock()
			}
			if _, e := next.RegisterReviewedReplacement(context.Background(), old, strings.Repeat("c", 64)); e == nil {
				t.Fatal("unsafe replacement")
			}
		})
	}
}

func TestRegisteredReplacementRacesActualIntentConsumption(t *testing.T) {
	for i := 0; i < 20; i++ {
		old := attemptFixture(t)
		next := replacementFixture(t, old)
		var credentialCalls atomic.Int64
		ready := make(chan struct{})
		registered := make(chan error, 1)
		executed := make(chan error, 1)
		go func() {
			<-ready
			_, e := next.RegisterReviewedReplacement(context.Background(), old, strings.Repeat("c", 64))
			registered <- e
		}()
		go func() {
			<-ready
			_, e := old.Apply(context.Background(), old.ReviewDigest(), false, func() (ssh.Signer, error) { credentialCalls.Add(1); return nil, nil })
			executed <- e
		}()
		close(ready)
		re, ae := <-registered, <-executed
		if re == nil && ae == nil {
			t.Fatal("replacement and superseded transfer both accepted")
		}
		if re == nil {
			if credentialCalls.Load() != 0 {
				t.Fatal("replacement succeeded after old credential resolution")
			}
			if _, e := os.Stat(filepath.Join(old.dir, old.plan.Manifest.ID+".intent.json")); !os.IsNotExist(e) {
				t.Fatal("replacement succeeded with consumed old intent", e)
			}
			if _, e := old.Status(); e == nil {
				t.Fatal("old review survived replacement")
			}
		}
	}
}
