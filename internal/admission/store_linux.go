// Package admission implements an offline, conservative allocation model.
// It is not connected to a provider and cannot enforce live infrastructure.
package admission

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var (
	ErrDenied    = errors.New("admission denied")
	ErrConflict  = errors.New("request identity conflict")
	ErrUncertain = errors.New("store unavailable after uncertain persistence")
)

// Resources uses integer millicores, MiB and runner counts. A zero disk budget
// means the disk dimension is not budgeted and constrains nothing; a zero disk
// request or reservation means the profile did not specify disk.
type Resources struct {
	CPU     int64 `json:"cpu_millis"`
	RAM     int64 `json:"ram_mib"`
	Disk    int64 `json:"disk_mib"`
	Runners int64 `json:"runners"`
}

type Host struct {
	Budget     Resources `json:"budget"`
	Mode       string    `json:"mode"`
	Healthy    bool      `json:"healthy"`
	ObservedAt time.Time `json:"observed_at"`
	// CPUCapMillis is an optional fair-use sustained CPU display cap. Zero means
	// unset. It does not constrain admission yet; creation enforcement is a later
	// slice. When positive it must not exceed the host CPU budget.
	CPUCapMillis int64 `json:"cpu_cap_millis,omitempty"`
}

type Pool struct {
	Budget Resources `json:"budget"`
	Hosts  []string  `json:"hosts"`
}

type Config struct {
	Global            Resources       `json:"global"`
	Hosts             map[string]Host `json:"hosts"`
	Pools             map[string]Pool `json:"pools"`
	MaxObservationAge time.Duration   `json:"max_observation_age_ns"`
}

type Request struct {
	Key       string    `json:"key"`
	Pool      string    `json:"pool"`
	Host      string    `json:"host"`
	Resources Resources `json:"resources"`
}

type Reservation struct {
	Request   Request   `json:"request"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
}

type Decision struct {
	Simulation  bool        `json:"simulation"`
	Replay      bool        `json:"replay"`
	Reservation Reservation `json:"reservation"`
}

type Ledger struct {
	Version      int                    `json:"version"`
	Config       Config                 `json:"config"`
	Reservations map[string]Reservation `json:"reservations"`
}

// Store uses a private local directory and a stable sibling flock file.
// Never remove or replace the lock file while any caller might be using it.
type Store struct {
	mu       sync.Mutex
	path     string
	poisoned bool
	now      func() time.Time
}

// Open initializes an empty ledger only when it does not already exist.
// Existing policy is persisted; passing new config does not overwrite it.
func Open(path string, initial Config) (*Store, error) {
	s := &Store{path: path, now: time.Now}
	err := s.withLock(func() error {
		_, err := os.Stat(s.path)
		if err == nil {
			_, err = s.load()
			return err
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := validateConfig(initial); err != nil {
			return err
		}
		return s.save(Ledger{Version: 1, Config: initial, Reservations: map[string]Reservation{}})
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) withLock(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return ErrUncertain
	}
	lock, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

func (s *Store) load() (Ledger, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return Ledger{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > 16<<20 {
		return Ledger{}, errors.New("ledger unavailable or too large")
	}
	var l Ledger
	d := json.NewDecoder(io.LimitReader(f, 16<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&l); err != nil {
		return Ledger{}, fmt.Errorf("invalid ledger: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return Ledger{}, errors.New("invalid trailing ledger data")
	}
	if l.Version != 1 || l.Reservations == nil {
		return Ledger{}, errors.New("invalid ledger version or reservations")
	}
	if err := validateConfig(l.Config); err != nil {
		return Ledger{}, err
	}
	for key, reservation := range l.Reservations {
		if key != reservation.Request.Key || !validRequest(reservation.Request) || !validState(reservation.State) {
			return Ledger{}, errors.New("invalid stored reservation")
		}
		if _, ok := l.Config.Hosts[reservation.Request.Host]; !ok {
			return Ledger{}, errors.New("reservation references unknown host")
		}
		if _, ok := l.Config.Pools[reservation.Request.Pool]; !ok {
			return Ledger{}, errors.New("reservation references unknown pool")
		}
	}
	return l, nil
}

func (s *Store) save(l Ledger) (err error) {
	// Any failed persistence makes the current Store refuse subsequent operations.
	defer func() {
		if err != nil {
			s.poisoned = true
		}
	}()
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".capacity-ledger-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), s.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Admit returns a simulation result only. Replay is a lookup, not permission
// to repeat an external create. No caller should invoke a provider from this API.
func (s *Store) Admit(req Request) (Decision, error) {
	var result Decision
	err := s.withLock(func() error {
		l, err := s.load()
		if err != nil {
			return err
		}
		if !validRequest(req) {
			return fmt.Errorf("%w: invalid request", ErrDenied)
		}
		if existing, ok := l.Reservations[req.Key]; ok {
			if existing.Request != req {
				return ErrConflict
			}
			result = Decision{Simulation: true, Replay: true, Reservation: existing}
			return nil
		}
		host, ok := l.Config.Hosts[req.Host]
		if !ok {
			return fmt.Errorf("%w: unknown host", ErrDenied)
		}
		pool, ok := l.Config.Pools[req.Pool]
		if !ok || !contains(pool.Hosts, req.Host) {
			return fmt.Errorf("%w: pool or host mapping", ErrDenied)
		}
		now := s.now()
		age := now.Sub(host.ObservedAt)
		if host.Mode != "enabled" || !host.Healthy || age < 0 || age > l.Config.MaxObservationAge {
			return fmt.Errorf("%w: host disabled, draining, unhealthy or stale", ErrDenied)
		}
		var global, hostUsed, poolUsed Resources
		for _, r := range l.Reservations {
			if !accumulate(&global, r.Request.Resources) {
				return fmt.Errorf("%w: accounting overflow", ErrDenied)
			}
			if r.Request.Host == req.Host && !accumulate(&hostUsed, r.Request.Resources) {
				return fmt.Errorf("%w: host accounting overflow", ErrDenied)
			}
			if r.Request.Pool == req.Pool && !accumulate(&poolUsed, r.Request.Resources) {
				return fmt.Errorf("%w: pool accounting overflow", ErrDenied)
			}
		}
		if !fits(global, req.Resources, l.Config.Global) || !fits(hostUsed, req.Resources, host.Budget) || !fits(poolUsed, req.Resources, pool.Budget) {
			return fmt.Errorf("%w: capacity exhausted", ErrDenied)
		}
		reservation := Reservation{Request: req, State: "creating", CreatedAt: now}
		l.Reservations[req.Key] = reservation
		if err := s.save(l); err != nil {
			return err
		}
		result = Decision{Simulation: true, Reservation: reservation}
		return nil
	})
	return result, err
}

// RecordState changes visibility only. All states keep their full allocation.
func (s *Store) RecordState(key, state string) error {
	return s.withLock(func() error {
		if !validState(state) {
			return errors.New("unsupported state; release is not implemented")
		}
		l, err := s.load()
		if err != nil {
			return err
		}
		r, ok := l.Reservations[key]
		if !ok {
			return errors.New("unknown reservation")
		}
		r.State = state
		l.Reservations[key] = r
		return s.save(l)
	})
}

func (s *Store) Snapshot() (Ledger, error) {
	var l Ledger
	err := s.withLock(func() error {
		var err error
		l, err = s.load()
		return err
	})
	return l, err
}

func nonnegative(r Resources) bool {
	return r.CPU >= 0 && r.RAM >= 0 && r.Disk >= 0 && r.Runners >= 0
}

func validRequest(r Request) bool {
	return len(r.Key) > 0 && len(r.Key) <= 256 && len(r.Host) > 0 && len(r.Pool) > 0 && r.Resources.CPU > 0 && r.Resources.RAM > 0 && r.Resources.Disk >= 0 && r.Resources.Runners == 1
}

func validState(state string) bool {
	switch state {
	case "creating", "idle", "running", "failed", "unknown", "deleting":
		return true
	}
	return false
}

func validateConfig(c Config) error {
	if !nonnegative(c.Global) || c.MaxObservationAge <= 0 || len(c.Hosts) == 0 || len(c.Pools) == 0 {
		return errors.New("invalid policy")
	}
	for name, h := range c.Hosts {
		if name == "" || !nonnegative(h.Budget) || h.CPUCapMillis < 0 || (h.Mode != "enabled" && h.Mode != "draining" && h.Mode != "disabled") || h.ObservedAt.IsZero() {
			return errors.New("invalid host policy")
		}
	}
	for name, p := range c.Pools {
		if name == "" || !nonnegative(p.Budget) || len(p.Hosts) == 0 {
			return errors.New("invalid pool policy")
		}
		for _, host := range p.Hosts {
			if _, ok := c.Hosts[host]; !ok {
				return errors.New("pool references unknown host")
			}
		}
	}
	return nil
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func fits(used, request, budget Resources) bool {
	// Subtraction after the bounds check avoids integer overflow. CPU, RAM and
	// runner budgets keep the legacy fail-closed semantics: a zero budget denies
	// any positive request. Disk follows the v10 semantics: a zero disk budget
	// means the dimension is not budgeted and constrains nothing.
	return fitsBounded(used.CPU, request.CPU, budget.CPU) && fitsBounded(used.RAM, request.RAM, budget.RAM) && fitsBounded(used.Runners, request.Runners, budget.Runners) && fitsDisk(used.Disk, request.Disk, budget.Disk)
}

func fitsBounded(used, request, budget int64) bool {
	return used <= budget && request <= budget-used
}

func fitsDisk(used, request, budget int64) bool {
	if budget == 0 {
		return true
	}
	return used <= budget && request <= budget-used
}

func accumulate(total *Resources, value Resources) bool {
	const max = int64(1<<63 - 1)
	if value.CPU > max-total.CPU || value.RAM > max-total.RAM || value.Disk > max-total.Disk || value.Runners > max-total.Runners {
		return false
	}
	total.CPU += value.CPU
	total.RAM += value.RAM
	total.Disk += value.Disk
	total.Runners += value.Runners
	return true
}
