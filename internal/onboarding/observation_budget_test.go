package onboarding

import (
	"context"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

type countingBindingStore struct {
	Store
	reads int
}

func (s *countingBindingStore) ReadOnboardingBindings(ctx context.Context) (map[string]admission.HostBinding, error) {
	s.reads++
	return s.Store.ReadOnboardingBindings(ctx)
}
func TestLateSuccessfulIdentityIsNotAvailableObservation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "canceled"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			db := storeFixture(t)
			store := &countingBindingStore{Store: db}
			fixture := testsupport.Identity(t, "private-late-identity")
			identity, err := fixture.Client.PhysicalIdentity(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			before := snapshot(t, db)
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
			}
			defer cancel()
			calls := 0
			observer := observerFunc(func(read context.Context) (incusapi.PhysicalServerIdentity, error) {
				calls++
				if deadline {
					<-read.Done()
				} else {
					cancel()
				}
				return identity, nil
			})
			manager, err := New(store, map[string]Observer{"candidate-b": observer}, true)
			if err != nil {
				t.Fatal(err)
			}
			result, err := manager.Probe(ctx, Request{requestID, "candidate-b", "late observation check"})
			if err != nil {
				t.Fatal(err)
			}
			if result.ObservationAvailable || result.ServerVersion != "" || result.BindingClass != "not_checked" || store.reads != 0 {
				t.Fatalf("late success presented as current observation: %+v, binding reads%d", result, store.reads)
			}
			if result.EnrollmentPerformed || result.ActivationPerformed || result.CapacityQualified || calls != 1 {
				t.Fatal("late probe changed authority or repeated")
			}
			after := snapshot(t, db)
			unchangedExceptAudit(t, before, after)
			if len(after.Audit) != len(before.Audit)+2 {
				t.Fatal("lost request/completed audit")
			}
		})
	}
}
