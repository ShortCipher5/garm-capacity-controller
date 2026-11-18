//go:build linux

package nativeplanning

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capsulepeer"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type Choice struct {
	ReserveCPUMilli int64                     `json:"reserve_cpu_milli"`
	ReserveRAMMiB   int64                     `json:"reserve_ram_mib"`
	Limits          nativeboundary.Limits     `json:"limits"`
	Partition       capsulepeer.PartitionSpec `json:"partition"`
}
type Proposal struct {
	Binding           Binding             `json:"binding"`
	Observation       sshinventory.Result `json:"observation"`
	ObservationDigest string              `json:"observation_digest"`
	ExpiresAt         time.Time           `json:"expires_at"`
	Defaults          Choice              `json:"defaults"`
	ReviewDigest      string              `json:"review_digest"`
	Warnings          []string            `json:"warnings"`
}
type Saved struct {
	Selection       Selection `json:"selection"`
	SelectionDigest string    `json:"selection_digest"`
	State           string    `json:"state"`
}
type cached struct {
	proposal Proposal
	began    time.Time
}
type Service struct {
	mu        sync.Mutex
	inventory *sshinventory.Service
	bindings  map[string]Binding
	cache     map[string]cached
	directory string
}

func protectedDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrSelection
	}
	for p := path; ; p = filepath.Dir(p) {
		s, e := os.Lstat(p)
		if e != nil || !s.IsDir() || s.Mode().Perm()&0022 != 0 {
			return ErrSelection
		}
		st, ok := s.Sys().(*syscall.Stat_t)
		if !ok || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) {
			return ErrSelection
		}
		if p == "/" {
			return nil
		}
	}
}
func NewService(inventory *sshinventory.Service, bindings map[string]Binding, directory string) (*Service, error) {
	if inventory == nil || len(bindings) < 1 || len(bindings) > 8 || protectedDirectory(directory) != nil {
		return nil, ErrSelection
	}
	owned := map[string]Binding{}
	seen := map[string]bool{}
	for id, b := range bindings {
		if !label.MatchString(id) || ValidateBinding(b) != nil || seen[b.PreparationID] {
			return nil, ErrSelection
		}
		owned[id] = b
		seen[b.PreparationID] = true
	}
	return &Service{inventory: inventory, bindings: owned, cache: map[string]cached{}, directory: directory}, nil
}
func (s *Service) Bindings() map[string]Binding {
	out := map[string]Binding{}
	for id, b := range s.bindings {
		out[id] = b
	}
	return out
}
func (s *Service) current(ctx context.Context, b Binding) error {
	targets, e := s.inventory.ListTargets(ctx)
	if e != nil {
		return ErrSelection
	}
	matches := 0
	for _, t := range targets {
		if t.ID == b.InventoryTarget.ID {
			if t != b.InventoryTarget {
				return ErrSelection
			}
			matches++
		}
	}
	if matches != 1 {
		return ErrSelection
	}
	return nil
}
func defaults(b Binding, o sshinventory.Result) (Choice, []string, error) {
	if o.Source != "authenticated_linux_os_view" || o.PhysicalCapacityQualified || o.LogicalCPUs == nil || o.RAMMiB == nil || *o.LogicalCPUs < 1 || *o.LogicalCPUs > 1000000 || *o.RAMMiB < 2048 || *o.RAMMiB > 1<<40 {
		return Choice{}, nil, ErrSelection
	}
	cpu := *o.LogicalCPUs * 1000
	reserveCPU := cpu / 10
	if reserveCPU < 100 {
		reserveCPU = 100
	}
	reserveRAM := *o.RAMMiB / 5
	if reserveRAM < 2048 {
		reserveRAM = 2048
	}
	if reserveRAM > *o.RAMMiB/2 {
		reserveRAM = *o.RAMMiB / 2
	}
	memory := *o.RAMMiB - reserveRAM
	warnings := []string{"os_view_not_physical_authority", "storage_identity_requires_explicit_configuration"}
	if o.AvailableRAMMiB != nil {
		available := *o.AvailableRAMMiB - reserveRAM
		if available < memory {
			memory = available
		}
	} else {
		warnings = append(warnings, "available_memory_unknown")
	}
	if memory < 1024 || cpu-reserveCPU < 400 {
		return Choice{}, nil, ErrSelection
	}
	limits := nativeboundary.Limits{MemoryBytes: memory * MiB, CPUMilli: cpu - reserveCPU, Tasks: 4096}
	partition := capsulepeer.PartitionSpec{Bootstrap: nativeboundary.Limits{MemoryBytes: 128 * MiB, CPUMilli: 100, Tasks: 32}, Daemon: nativeboundary.Limits{MemoryBytes: 512 * MiB, CPUMilli: 100, Tasks: 128}, Monitors: nativeboundary.Limits{MemoryBytes: 128 * MiB, CPUMilli: 100, Tasks: 64}, Guests: nativeboundary.Limits{MemoryBytes: (memory - 768) * MiB, CPUMilli: limits.CPUMilli - 300, Tasks: 3872}}
	choice := Choice{reserveCPU, reserveRAM, limits, partition}
	sample := selection(b, o, choice, o.ObservedAt)
	if ValidateSelection(sample) != nil {
		return Choice{}, nil, ErrSelection
	}
	return choice, warnings, nil
}
func selection(b Binding, o sshinventory.Result, c Choice, now time.Time) Selection {
	return Selection{Version: Version, Binding: b, Observation: o, ObservationDigest: ObservationDigest(b, o), SavedAt: now, ExpiresAt: o.ObservedAt.Add(Lifetime), ReserveCPUMilli: c.ReserveCPUMilli, ReserveRAMMiB: c.ReserveRAMMiB, Limits: c.Limits, Partition: c.Partition}
}
func cloneProposal(p Proposal) Proposal {
	raw, _ := json.Marshal(p)
	var out Proposal
	json.Unmarshal(raw, &out)
	return out
}

// Probe invokes the actual pinned inventory service and its durable read audit.
// No caller-supplied observation can enter the review cache.
func (s *Service) Probe(ctx context.Context, id, bindingDigest, requestID string) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bindings[id]
	if !ok || bindingDigest != Digest(b) || s.current(ctx, b) != nil {
		return Proposal{}, ErrSelection
	}
	began := time.Now()
	result, e := s.inventory.Probe(ctx, b.InventoryTarget.ID, b.InventoryTarget.ReviewDigest, requestID)
	if e != nil || result.TargetID != b.InventoryTarget.ID || result.ReviewDigest != b.InventoryTarget.ReviewDigest || s.current(ctx, b) != nil {
		return Proposal{}, ErrSelection
	}
	o := result.Inventory
	if o.ObservedAt.Before(began.Add(-time.Second)) || o.ObservedAt.After(time.Now()) || time.Since(began) > Lifetime {
		return Proposal{}, ErrSelection
	}
	choice, warnings, e := defaults(b, o)
	if e != nil {
		return Proposal{}, e
	}
	p := Proposal{Binding: b, Observation: o, ObservationDigest: ObservationDigest(b, o), ExpiresAt: o.ObservedAt.Add(Lifetime), Defaults: choice, Warnings: warnings}
	p.ReviewDigest = Digest(p)
	s.cache[id] = cached{cloneProposal(p), began}
	return cloneProposal(p), nil
}

// Save persists an explicitly selected configuration only. It neither changes
// budgets nor writes a preparation bundle; the offline builder consumes it.
func (s *Service) Save(ctx context.Context, id, review string, c Choice) (Saved, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.cache[id]
	if !ok || review != item.proposal.ReviewDigest || time.Since(item.began) >= Lifetime || s.current(ctx, item.proposal.Binding) != nil || ctx.Err() != nil {
		return Saved{}, ErrSelection
	}
	p := item.proposal
	existing, e := s.saved(p.Binding)
	if e != nil || len(existing) >= 64 {
		return Saved{}, ErrSelection
	}
	value := selection(p.Binding, p.Observation, c, time.Now().UTC())
	if ValidateFreshSelection(value, time.Now()) != nil {
		return Saved{}, ErrSelection
	}
	hash := Digest(value)
	raw, e := json.Marshal(value)
	if e != nil || len(raw) > 16384 || protectedDirectory(s.directory) != nil {
		return Saved{}, ErrSelection
	}
	dir, e := os.Open(s.directory)
	if e != nil {
		return Saved{}, ErrSelection
	}
	names, e := dir.Readdirnames(1025)
	dir.Close()
	if e != nil && e != io.EOF || len(names) >= 1024 {
		return Saved{}, ErrSelection
	}
	name := "selection-" + p.Binding.PreparationID + "-" + hash + ".json"
	f, e := os.OpenFile(filepath.Join(s.directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0600)
	if e != nil {
		return Saved{}, ErrSelection
	}
	e = f.Chmod(0600)
	if e == nil {
		_, e = f.Write(raw)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil || closeErr != nil {
		return Saved{}, ErrSelection
	}
	dir, e = os.Open(s.directory)
	if e != nil {
		return Saved{}, ErrSelection
	}
	e = dir.Sync()
	dir.Close()
	if e != nil {
		return Saved{}, ErrSelection
	}
	var owned Selection
	json.Unmarshal(raw, &owned)
	return Saved{owned, hash, "saved_configuration_not_applied"}, nil
}

// ValidateDirectory performs read-only deployment configuration validation.
func ValidateDirectory(path string) error { return protectedDirectory(path) }
