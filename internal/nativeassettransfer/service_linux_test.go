//go:build linux

package nativeassettransfer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassets"
	"golang.org/x/crypto/ssh"
)

func awaitAttempt(t *testing.T, s *Service) State {
	t.Helper()
	end := time.Now().Add(30 * time.Second)
	for time.Now().Before(end) {
		v, e := s.Status()
		if e == nil && v.Attempt != nil && v.Attempt.CompletedAt != nil {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("attempt did not finish")
	return State{}
}
func TestWorkflowActualSSHRetainsIntentAndHistoricalReceipt(t *testing.T) {
	b, target, key, journal := fixture(t)
	host := signer(t)
	dial, done := server(t, b, &target, key, "prepare", host)
	s, e := NewService(b, target, journal)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.execute = func(ctx context.Context, p Plan, d string, k ssh.Signer, inspect bool) (nativeassets.Receipt, error) {
		return b.execute(ctx, p, d, k, inspect, dial)
	}
	resolve := func() (ssh.Signer, error) { return key, nil }
	accepted, e := s.Start(context.Background(), s.ReviewDigest(), false, resolve)
	if e != nil || accepted.Status != "unknown" || accepted.Attempt == nil || accepted.Attempt.Sequence != 1 {
		t.Fatal(e, accepted)
	}
	if _, e := os.Stat(filepath.Join(journal, b.manifest.ID+".intent.json")); e != nil {
		t.Fatal("202 before durable intent", e)
	}
	state := awaitAttempt(t, s)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if state.Status != "artifacts_prepared_not_activated" || state.Attempt.Status != state.Status || state.Receipt == nil || state.Receipt.ActivationAuthorized {
		t.Fatal(state)
	}
	// Reconstructed service and CLI both honor the identical permanent Plan intent.
	restored, e := NewService(b, target, journal)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := restored.Start(context.Background(), s.ReviewDigest(), false, resolve); e == nil {
		t.Fatal("restarted service replayed upload")
	}
	if _, e := b.Transfer(context.Background(), s.plan, s.ReviewDigest(), key, journal); e == nil {
		t.Fatal("CLI replayed workflow intent")
	}
	restored.execute = func(context.Context, Plan, string, ssh.Signer, bool) (nativeassets.Receipt, error) {
		return nativeassets.Receipt{}, ErrUnknown
	}
	if _, e := restored.Start(context.Background(), s.ReviewDigest(), true, resolve); e != nil {
		t.Fatal(e)
	}
	state = awaitAttempt(t, restored)
	if state.Receipt == nil || state.Attempt.Sequence != 2 || state.Attempt.Status != "observation_unavailable" || state.Attempt.Receipt != nil {
		t.Fatal("old receipt was confused with latest inspect", state)
	}
	restored.Close()
}
func TestWorkflowCloseJoinsCancelledAttemptAndNoOrphanAdoption(t *testing.T) {
	b, target, key, journal := fixture(t)
	s, e := NewService(b, target, journal)
	if e != nil {
		t.Fatal(e)
	}
	entered := make(chan struct{})
	done := make(chan struct{})
	s.execute = func(ctx context.Context, _ Plan, _ string, _ ssh.Signer, _ bool) (nativeassets.Receipt, error) {
		close(entered)
		<-ctx.Done()
		close(done)
		return nativeassets.Receipt{}, ErrUnknown
	}
	if _, e := s.Start(context.Background(), s.ReviewDigest(), false, func() (ssh.Signer, error) { return key, nil }); e != nil {
		t.Fatal(e)
	}
	<-entered
	s.Close()
	select {
	case <-done:
	default:
		t.Fatal("shutdown not joined")
	}
	state, e := s.Status()
	if e != nil || state.Status != "unknown" || state.Attempt.Status != "observation_unavailable" {
		t.Fatal(e, state)
	}
	if _, e := s.Start(context.Background(), s.ReviewDigest(), true, func() (ssh.Signer, error) { return key, nil }); e == nil {
		t.Fatal("closed service accepted")
	}
	// Removing the root intent while leaving attempt evidence must not allow a new upload.
	os.Remove(filepath.Join(journal, b.manifest.ID+".intent.json"))
	if _, e := s.Status(); e == nil {
		t.Fatal("orphan attempt adopted")
	}
}
func TestWorkflowHistoricalReceiptNeedsCanonicalIntent(t *testing.T) {
	b, target, _, journal := fixture(t)
	s, e := NewService(b, target, journal)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	receipt := nativeassets.Receipt{Version: nativeassets.Version, ID: b.manifest.ID, ManifestDigest: manifestDigest(b.manifest), State: "artifacts_prepared_not_activated"}
	raw, _ := json.Marshal(State{ReviewDigest: s.ReviewDigest(), Status: receipt.State, Receipt: &receipt})
	os.WriteFile(filepath.Join(journal, b.manifest.ID+".receipt.json"), raw, 0600)
	if _, e := s.Status(); e == nil {
		t.Fatal("orphan receipt adopted")
	}
}
