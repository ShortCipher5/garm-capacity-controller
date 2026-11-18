package sshinventory

import (
	"context"
	"errors"
	"golang.org/x/crypto/ssh"
	"testing"
)

func TestServiceBindsEntireReviewedTupleAndAuditsBeforeProbe(t *testing.T) {
	config, resolve, _ := server(t, sample)
	phases := []string{}
	requested := false
	guarded := func(ctx context.Context, ref string) (ssh.Signer, error) {
		if !requested {
			t.Error("credential resolved before requested audit")
		}
		return resolve(ctx, ref)
	}
	configs := map[string]Config{"candidate": config}
	service, e := NewService(configs, guarded, func(_ context.Context, request, target, digest, phase string, available bool) error {
		phases = append(phases, phase)
		if phase == "requested" {
			requested = true
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	target := service.Targets()[0]
	changed := config
	changed.Address = "192.0.2.2"
	configs["candidate"] = changed
	if _, e = service.Probe(context.Background(), "candidate", "bad", "11111111-1111-4111-8111-111111111111"); !errors.Is(e, ErrReview) || len(phases) != 0 {
		t.Fatal("unreviewed target probed")
	}
	result, e := service.Probe(context.Background(), target.ID, target.ReviewDigest, "11111111-1111-4111-8111-111111111111")
	if e != nil || result.TargetID != "candidate" || !result.Inventory.Complete || len(phases) != 2 || phases[0] != "requested" || phases[1] != "completed" {
		t.Fatalf("%+v %v %v", result, e, phases)
	}
	altered, e := NewService(map[string]Config{"candidate": changed}, resolve, func(context.Context, string, string, string, string, bool) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	if altered.Targets()[0].ReviewDigest == target.ReviewDigest {
		t.Fatal("destination change did not invalidate review")
	}
}
func TestFailedAuditPreventsCredentialUse(t *testing.T) {
	config, _, _ := server(t, sample)
	called := false
	service, e := NewService(map[string]Config{"candidate": config}, func(context.Context, string) (ssh.Signer, error) { called = true; return nil, ErrInventory }, func(context.Context, string, string, string, string, bool) error { return ErrInventory })
	if e != nil {
		t.Fatal(e)
	}
	target := service.Targets()[0]
	if _, e = service.Probe(context.Background(), target.ID, target.ReviewDigest, "11111111-1111-4111-8111-111111111111"); e != ErrInventory || called {
		t.Fatal("audit failure reached transport")
	}
}
