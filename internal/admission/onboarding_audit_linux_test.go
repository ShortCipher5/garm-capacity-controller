package admission

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOnboardingAuditIsBoundedAtomicAndDoesNotReopenOrChangeLedger(t *testing.T) {
	ctx := context.Background()
	store := sqliteFixtureStore(t, fixture(time.Now()), true)
	before := sqliteSnapshot(t, store)
	record := OnboardingProbeAudit{RequestID: "11111111-1111-4111-8111-111111111111", ProfileID: "candidate-b", Reason: "read-only check", Phase: "requested"}
	if err := store.RecordOnboardingProbeAudit(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.Phase = "completed"
	record.ObservationAvailable = true
	record.ServerVersion = "6.0.4"
	record.BindingClass = "no_match_observed"
	if err := store.RecordOnboardingProbeAudit(ctx, record); err != nil {
		t.Fatal(err)
	}
	after := sqliteSnapshot(t, store)
	if after.Ready != before.Ready || after.Epoch != before.Epoch || !reflect.DeepEqual(after.Config, before.Config) || !reflect.DeepEqual(after.HostBindings, before.HostBindings) || !reflect.DeepEqual(after.Reservations, before.Reservations) || len(after.Audit) != len(before.Audit)+2 {
		t.Fatal("read audit changed admission/binding/resource state")
	}
	for _, bad := range []OnboardingProbeAudit{{RequestID: record.RequestID, ProfileID: "https://not-a-profile.invalid", Reason: "test", Phase: "requested"}, {RequestID: record.RequestID, ProfileID: record.ProfileID, Reason: strings.Repeat("x", 257), Phase: "requested"}, {RequestID: record.RequestID, ProfileID: record.ProfileID, Reason: "test", Phase: "activated"}} {
		if store.RecordOnboardingProbeAudit(ctx, bad) == nil {
			t.Fatal("invalid audit record accepted")
		}
	}
	if _, err := store.db.Exec("CREATE TRIGGER reject_onboarding BEFORE INSERT ON audit WHEN NEW.kind LIKE 'onboarding_probe_%' BEGIN SELECT RAISE(ABORT,'denied'); END"); err != nil {
		t.Fatal(err)
	}
	prior := sqliteSnapshot(t, store)
	if store.RecordOnboardingProbeAudit(ctx, record) == nil {
		t.Fatal("failed audit reported success")
	}
	final := sqliteSnapshot(t, store)
	if len(prior.Audit) != len(final.Audit) || !reflect.DeepEqual(prior.Config, final.Config) {
		t.Fatal("failed audit partially mutated state")
	}
}
