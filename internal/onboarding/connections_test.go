package onboarding

import (
	"context"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type connectionObserver struct{}

func (connectionObserver) PhysicalIdentity(context.Context) (incusapi.PhysicalServerIdentity, error) {
	return incusapi.PhysicalServerIdentity{}, nil
}
func TestConnectionServiceScopesApprovalAndCancelsRevokedReads(t *testing.T) {
	budget := admission.Resources{CPU: 1000, RAM: 2048, Runners: 1}
	s, err := admission.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"), admission.Config{Global: budget, Hosts: map[string]admission.Host{"host": {Budget: budget, Mode: "disabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool": {Budget: budget, Hosts: []string{"host"}}}, MaxObservationAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	calls := 0
	closed := 0
	targets := map[string]ConnectionTarget{"alias": {Endpoint: "https://192.0.2.10:8443", Project: "runners", Factory: func(admission.Connection) (Observer, func(), error) {
		calls++
		return connectionObserver{}, func() { closed++ }, nil
	}}}
	service, err := NewConnectionService(s, targets, []string{"static"})
	if err != nil {
		t.Fatal(err)
	}
	c := admission.Connection{ID: "candidate", Endpoint: targets["alias"].Endpoint, Project: "runners", CredentialRef: "alias", ServerKeySHA256: strings.Repeat("a", 64)}
	ctx := context.Background()
	bad := c
	bad.Endpoint = "https://192.0.2.11:8443"
	if _, err = service.Put(ctx, bad, 0); !errors.Is(err, ErrDenied) {
		t.Fatal("credential escaped target policy")
	}
	bad = c
	bad.ID = "static"
	if _, err = service.Put(ctx, bad, 0); !errors.Is(err, ErrDenied) {
		t.Fatal("static profile replaced")
	}
	c, err = service.Put(ctx, c, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err = service.Resolve(ctx, c.ID); !errors.Is(err, ErrDenied) || calls != 0 {
		t.Fatal("draft used credentials")
	}
	c, err = service.Transition(ctx, c.ID, c.Revision, "approved", c.ServerKeySHA256)
	if err != nil || calls != 0 {
		t.Fatal("approval contacted host", err)
	}
	read, _, revision, close, err := service.Resolve(ctx, c.ID)
	if err != nil || revision != c.Revision || calls != 1 {
		t.Fatal(err)
	}
	if _, err = service.Transition(ctx, c.ID, c.Revision, "revoked", ""); err != nil {
		t.Fatal(err)
	}
	if read.Err() != context.Canceled {
		t.Fatal("revocation left read running")
	}
	close()
	if closed != 1 {
		t.Fatal("transport not closed")
	}
	if _, _, _, _, err = service.Resolve(ctx, c.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked connection resolved")
	}
}
