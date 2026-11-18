//go:build linux

package nativeprovision

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitialRealPlanPinsFirstBundleAndReplacementChain(t *testing.T) {
	s := initialPlanFixture(t)
	registration, selection := strings.Repeat("c", 64), strings.Repeat("d", 64)
	initial, e := s.RegisterInitialPlan(t.Context(), registration, selection)
	if e != nil {
		t.Fatal(e)
	}
	got, e := ReadInitialPlan(s.dir, s.PreparationID(), s.plan.Target)
	if e != nil || *got != initial {
		t.Fatal(got, e)
	}
	again, e := s.RegisterInitialPlan(t.Context(), registration, selection)
	if e != nil || again != initial {
		t.Fatal(again, e)
	}
	next := replacementFixture(t, s)
	if _, e = next.Status(); e == nil {
		t.Fatal("unreviewed first-plan replacement")
	}
	if _, e = next.RegisterInitialPlan(t.Context(), registration, selection); e == nil {
		t.Fatal("initial plan overwritten")
	}
	if _, e = next.RegisterReviewedReplacement(t.Context(), s, strings.Repeat("e", 64)); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Status(); e == nil {
		t.Fatal("old plan active")
	}
	if _, e = next.Status(); e != nil {
		t.Fatal(e)
	}
}
func TestInitialPlanRejectsConsumedAndCorruptHistory(t *testing.T) {
	for _, mode := range []string{"intent", "attempt", "corrupt", "chain", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s := initialPlanFixture(t)
			ctx := t.Context()
			switch mode {
			case "intent":
				os.WriteFile(filepath.Join(s.dir, s.PreparationID()+".intent.json"), []byte("{}"), 0600)
			case "attempt":
				os.WriteFile(filepath.Join(s.dir, s.PreparationID()+".attempt.000001.intent.json"), []byte("{}"), 0600)
			case "corrupt":
				os.WriteFile(filepath.Join(s.dir, s.PreparationID()+".initial.json"), []byte("{}"), 0600)
			case "chain":
				r := RegisteredPlan{1, s.PreparationID(), targetDigest(s.plan.Target), strings.Repeat("c", 64), s.ReviewDigest(), strings.Repeat("d", 64)}
				b, _ := json.Marshal(r)
				os.WriteFile(filepath.Join(s.dir, s.PreparationID()+".registered.000001.json"), b, 0600)
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, e := s.RegisterInitialPlan(ctx, strings.Repeat("c", 64), strings.Repeat("d", 64)); e == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func initialPlanFixture(t *testing.T) *Service {
	t.Helper()
	s := attemptFixture(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	p, err := os.MkdirTemp(cwd, ".initial-plan-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(); os.RemoveAll(p) })
	s.dir = p
	return s
}
