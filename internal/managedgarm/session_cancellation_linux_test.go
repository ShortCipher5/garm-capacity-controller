package managedgarm

import (
	"context"
	"testing"
	"time"
)

// This is an internal state-transition regression, not a minted Session or a
// Docker/topology qualification. Observe rejects these contexts before any
// filesystem, namespace, credential, or network access.
func TestInterruptedTopologyCheckPermanentlyRevokesSession(t *testing.T) {
	for _, kind := range []string{"cancelled", "expired"} {
		t.Run(kind, func(t *testing.T) {
			var ctx context.Context
			var cancel context.CancelFunc
			if kind == "cancelled" {
				ctx, cancel = context.WithCancel(context.Background())
				cancel()
			} else {
				ctx, cancel = context.WithDeadline(context.Background(), time.Unix(1, 0))
			}
			defer cancel()
			s := &Session{token: "internal-state-test-only", namespace: "unobserved-test-namespace"}
			if ctx.Err() == nil {
				t.Fatal("context must already be interrupted")
			}
			if err := s.Verify(ctx); err != ErrBoundary {
				t.Fatalf("interrupted check: %v", err)
			}
			if !s.revoked || s.token != "" {
				t.Fatal("interrupted observation retained session")
			}
			if err := s.Verify(context.Background()); err != ErrBoundary {
				t.Fatalf("fresh caller revived revoked session: %v", err)
			}
			if _, err := s.ReadCollection(context.Background(), "/api/v1/instances"); err != ErrBoundary {
				t.Fatalf("revoked collection read: %v", err)
			}
			if s.Identity() != "" || s.ControllerID() != "" {
				t.Fatal("revoked session exposed usable identity")
			}
		})
	}
}
