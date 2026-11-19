package brokerinput

import (
	"context"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"testing"
	"time"
)

func TestPlanFailureDoesNotExposeCause(t *testing.T) {
	secret := errors.New("private-host token=secret")
	if PlanFailureReason(unavailable("private-host token=secret", secret)) != "preview_unavailable" {
		t.Fatal("unlisted category leaked")
	}
	if PlanFailureReason(secret) != "preview_unavailable" {
		t.Fatal("raw error leaked")
	}
	err := unavailable("ledger_unavailable", secret)
	if err.Error() != "ledger_unavailable" || PlanFailureReason(err) != "ledger_unavailable" || !errors.Is(err, secret) {
		t.Fatal("classification or identity lost")
	}
}
func TestAdvisoryFailureAndExpiryReasons(t *testing.T) {
	s := &AdvisoryLoop{}
	if _, err := s.ReadPlan(context.Background()); PlanFailureReason(err) != "awaiting_observation" {
		t.Fatal(err)
	}
	s.failure = unavailable("pool_mapping_missing", capacitybroker.ErrSnapshot)
	if _, err := s.ReadPlan(context.Background()); PlanFailureReason(err) != "pool_mapping_missing" {
		t.Fatal(err)
	}
	s.plan = &ReadPlan{ExpiresAt: time.Now().Add(-time.Second)}
	if _, err := s.ReadPlan(context.Background()); PlanFailureReason(err) != "snapshot_changed_or_expired" {
		t.Fatal(err)
	}
}
