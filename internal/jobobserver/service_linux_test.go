//go:build linux

package jobobserver

import (
	"context"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"path/filepath"
	"testing"
	"time"
)

func TestEmptyDurableBindingsDoNotContactOwnersOrGitHub(t *testing.T) {
	budget := admission.Resources{CPU: 1000, RAM: 1024, Runners: 1}
	config := admission.Config{Global: budget, MaxObservationAge: time.Minute, Hosts: map[string]admission.Host{"a": {Budget: budget, Mode: "enabled", Healthy: true, ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"app": {Budget: budget, Hosts: []string{"a"}}}}
	store, e := admission.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"), config)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	// No transports exist: an empty real ledger must produce no reads or claims.
	s := &Service{store: store, interval: time.Second, after: "completed-page"}
	out, e := s.Cycle(context.Background())
	if e != nil || out.Examined != 0 || s.after != "" {
		t.Fatal(out, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = s.Run(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e = store.NativeRunnerBindings(context.Background(), "", 65); e == nil {
		t.Fatal("unbounded page")
	}
}
func TestMissingConfigurationRefuses(t *testing.T) {
	if _, e := New(Configuration{}); e == nil {
		t.Fatal("missing authority accepted")
	}
	var s *Service
	if _, e := s.Cycle(context.Background()); e == nil {
		t.Fatal("nil service accepted")
	}
}
