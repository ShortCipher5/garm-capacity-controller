package admission

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(now time.Time) Config {
	budget := Resources{CPU: 4000, RAM: 8192, Runners: 4}
	return Config{
		Global:            budget,
		MaxObservationAge: time.Minute,
		Hosts: map[string]Host{
			"a": {Budget: budget, Mode: "enabled", Healthy: true, ObservedAt: now},
			"b": {Budget: budget, Mode: "enabled", Healthy: true, ObservedAt: now},
		},
		Pools: map[string]Pool{
			"app": {Budget: budget, Hosts: []string{"a", "b"}},
			"api": {Budget: budget, Hosts: []string{"a", "b"}},
		},
	}
}

func request(key string) Request {
	return Request{Key: key, Pool: "app", Host: "a", Resources: Resources{CPU: 1000, RAM: 2048, Runners: 1}}
}

func newStore(t *testing.T, config Config) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.ledger.json"), config)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestConcurrentCrossPoolAndHostCap(t *testing.T) {
	s := newStore(t, fixture(time.Now()))
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := request(fmt.Sprint(i))
			if i%2 == 0 {
				r.Pool, r.Host = "api", "b"
			}
			if _, err := s.Admit(r); err == nil {
				admitted.Add(1)
			} else if !errors.Is(err, ErrDenied) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if admitted.Load() != 4 {
		t.Fatalf("admitted %d, want exactly 4 globally", admitted.Load())
	}
}

func TestIdempotentRestartAndConflict(t *testing.T) {
	c := fixture(time.Now())
	s := newStore(t, c)
	r := request("stable")
	first, err := s.Admit(r)
	if err != nil || first.Replay || !first.Simulation {
		t.Fatalf("initial result: %+v %v", first, err)
	}
	reopened, err := Open(s.path, c)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := reopened.Admit(r)
	if err != nil || !replay.Replay || replay.Reservation.Request != first.Reservation.Request || !replay.Reservation.CreatedAt.Equal(first.Reservation.CreatedAt) {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	r.Host = "b"
	if _, err := reopened.Admit(r); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed request accepted: %v", err)
	}
	l, err := reopened.Snapshot()
	if err != nil || len(l.Reservations) != 1 {
		t.Fatalf("wrong restart ledger: %+v %v", l, err)
	}
}

func TestStatesNeverReleaseCapacity(t *testing.T) {
	for _, state := range []string{"creating", "idle", "running", "failed", "unknown", "deleting"} {
		t.Run(state, func(t *testing.T) {
			c := fixture(time.Now())
			c.Global.Runners = 1
			s := newStore(t, c)
			if _, err := s.Admit(request("one")); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordState("one", state); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Admit(request("two")); !errors.Is(err, ErrDenied) {
				t.Fatalf("%s released capacity: %v", state, err)
			}
			if err := s.RecordState("one", "deleted"); err == nil {
				t.Fatal("unproven release accepted")
			}
		})
	}
}

func TestFailClosedHostAndPolicy(t *testing.T) {
	now := time.Now()
	cases := map[string]func(*Config){
		"draining":    func(c *Config) { h := c.Hosts["a"]; h.Mode = "draining"; c.Hosts["a"] = h },
		"disabled":    func(c *Config) { h := c.Hosts["a"]; h.Mode = "disabled"; c.Hosts["a"] = h },
		"unhealthy":   func(c *Config) { h := c.Hosts["a"]; h.Healthy = false; c.Hosts["a"] = h },
		"stale":       func(c *Config) { h := c.Hosts["a"]; h.ObservedAt = now.Add(-2 * time.Minute); c.Hosts["a"] = h },
		"future":      func(c *Config) { h := c.Hosts["a"]; h.ObservedAt = now.Add(time.Minute); c.Hosts["a"] = h },
		"zero budget": func(c *Config) { c.Global.CPU = 0 },
		"host cpu":    func(c *Config) { h := c.Hosts["a"]; h.Budget.CPU = 999; c.Hosts["a"] = h },
		"host ram":    func(c *Config) { h := c.Hosts["a"]; h.Budget.RAM = 2047; c.Hosts["a"] = h },
		"pool cap":    func(c *Config) { p := c.Pools["app"]; p.Budget.Runners = 0; c.Pools["app"] = p },
		"mapping":     func(c *Config) { p := c.Pools["app"]; p.Hosts = []string{"b"}; c.Pools["app"] = p },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			c := fixture(now)
			change(&c)
			s := newStore(t, c)
			s.now = func() time.Time { return now }
			if _, err := s.Admit(request("no")); !errors.Is(err, ErrDenied) {
				t.Fatalf("expected denial: %v", err)
			}
		})
	}
}

func TestInvalidRequestAndOverflow(t *testing.T) {
	s := newStore(t, fixture(time.Now()))
	for _, r := range []Request{
		{},
		{Key: "bad", Pool: "app", Host: "a", Resources: Resources{CPU: -1, RAM: 1, Runners: 1}},
		{Key: "bad", Pool: "app", Host: "a", Resources: Resources{CPU: 1, RAM: 1, Runners: 0}},
		{Key: "bad", Pool: "missing", Host: "a", Resources: Resources{CPU: 1, RAM: 1, Runners: 1}},
		{Key: "bad", Pool: "app", Host: "missing", Resources: Resources{CPU: 1, RAM: 1, Runners: 1}},
	} {
		if _, err := s.Admit(r); !errors.Is(err, ErrDenied) {
			t.Fatalf("invalid request accepted: %+v %v", r, err)
		}
	}
	const max = int64(1<<63 - 1)
	if fits(Resources{CPU: max}, Resources{CPU: 1}, Resources{CPU: max}) {
		t.Fatal("overflow fit")
	}
	total := Resources{CPU: max}
	if accumulate(&total, Resources{CPU: 1}) {
		t.Fatal("overflow accumulation")
	}
}

func TestCorruptionAndUncertainPersistence(t *testing.T) {
	s := newStore(t, fixture(time.Now()))
	original := s.path
	if err := os.WriteFile(original, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(request("no")); err == nil {
		t.Fatal("corrupt ledger accepted")
	}
	// Force a persistence failure without relying on Unix permissions or root.
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.save(Ledger{}); err == nil {
		t.Fatal("directory overwrite unexpectedly succeeded")
	}
	if _, err := s.Admit(request("blocked")); !errors.Is(err, ErrUncertain) {
		t.Fatalf("uncertain store not blocked: %v", err)
	}
}

func TestIndependentStoresSerialize(t *testing.T) {
	c := fixture(time.Now())
	c.Global.Runners = 1
	first := newStore(t, c)
	second, err := Open(first.path, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Admit(request("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Admit(request("second")); !errors.Is(err, ErrDenied) {
		t.Fatalf("independent Store bypassed cap: %v", err)
	}
}

func TestProcessWorker(t *testing.T) {
	path := os.Getenv("CAPACITY_TEST_LEDGER")
	if path == "" {
		return
	}
	s, err := Open(path, fixture(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Admit(request(os.Getenv("CAPACITY_TEST_KEY")))
	if err != nil && !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}

func TestCrossProcessAdmission(t *testing.T) {
	c := fixture(time.Now())
	c.Global.Runners = 2
	s := newStore(t, c)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestProcessWorker$")
			cmd.Env = append(os.Environ(), "CAPACITY_TEST_LEDGER="+s.path, "CAPACITY_TEST_KEY="+fmt.Sprint(i))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("worker: %v %s", err, out)
			}
		}(i)
	}
	wg.Wait()
	l, err := s.Snapshot()
	if err != nil || len(l.Reservations) != 2 {
		t.Fatalf("cross-process budget bypass: %d %v", len(l.Reservations), err)
	}
}
