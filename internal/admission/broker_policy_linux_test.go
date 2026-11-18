package admission

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrokerPolicyCASAndValidation(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	ctx := context.Background()
	p, err := s.BrokerPolicy(ctx)
	if err != nil || p.Revision != 0 || p.Repositories == nil {
		t.Fatalf("initial %+v %v", p, err)
	}
	p.Repositories = []BrokerRepositoryPolicy{{ID: "11111111-1111-4111-8111-111111111111", Weight: 1, MaxRunners: 5, Pools: []BrokerPoolPolicy{{ID: "22222222-2222-4222-8222-222222222222", MaxRunners: 4, WarmIdle: 1}}}}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.ReplaceBrokerPolicy(ctx, p)
			if err == nil {
				wins.Add(1)
			} else if !errors.Is(err, ErrBrokerConflict) {
				t.Errorf("write %v", err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("writers won %d", wins.Load())
	}
	got, err := s.BrokerPolicy(ctx)
	if err != nil || got.Revision != 1 || got.Repositories[0].Weight != 1 {
		t.Fatalf("read %+v %v", got, err)
	}
	if err := s.ReplaceBrokerPolicy(ctx, p); !errors.Is(err, ErrBrokerConflict) {
		t.Fatalf("lost response replay %v", err)
	}
	got.Repositories[0].Pools[0].WarmIdle = 5
	if err := s.ReplaceBrokerPolicy(ctx, got); !errors.Is(err, ErrDenied) {
		t.Fatalf("idle over cap %v", err)
	}
	got.Repositories[0].Pools[0].WarmIdle = 0
	got.Repositories = append(got.Repositories, got.Repositories[0])
	if ValidateBrokerPolicy(got) == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestBrokerPolicySurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	s, err := OpenSQLite(path, fixture(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	p := BrokerPolicy{Revision: 0, Repositories: []BrokerRepositoryPolicy{}}
	if err = s.ReplaceBrokerPolicy(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.BrokerPolicy(context.Background())
	if err != nil || got.Revision != 1 {
		t.Fatalf("reopen %+v %v", got, err)
	}
	if err = s.ReplaceBrokerPolicy(context.Background(), p); !errors.Is(err, ErrBrokerConflict) {
		t.Fatalf("restart replay %v", err)
	}
}
