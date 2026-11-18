//go:build linux

package nativeplanning

import (
	"context"
	"time"
)

// MixProposal is editable configuration derived from one actual saved selection.
// It neither proves physical capacity nor applies profiles, counts or pool limits.
type MixSize struct {
	Name  string `json:"name"`
	CPU   int64  `json:"cpu_millis"`
	RAM   int64  `json:"ram_mib"`
	Disk  int64  `json:"disk_mib"`
	Count int64  `json:"count"`
}
type MixProposal struct {
	Host               string    `json:"host"`
	SelectionExpiresAt time.Time `json:"selection_expires_at"`
	SelectionDigest    string    `json:"selection_digest"`
	Entries            []MixSize `json:"entries"`
	GuestCPU           int64     `json:"configured_guest_cpu_millis"`
	GuestRAM           int64     `json:"configured_guest_ram_mib"`
	DiskFitVerified    bool      `json:"disk_fit_verified"`
	State              string    `json:"state"`
}

// ProposeMix reopens the protected saved selection and checks its exact binding
// and freshness. Disk is an explicit operator choice, never inferred from root
// filesystem totals. Saving the proposal remains a separate reviewed operation.
func (s *Service) ProposeMix(ctx context.Context, id, digest string, diskMiB int64) (MixProposal, error) {
	if diskMiB < 1 || diskMiB > 1000000000 {
		return MixProposal{}, ErrSelection
	}
	_, selection, err := s.LookupSaved(ctx, id, digest)
	if err != nil {
		return MixProposal{}, err
	}
	p, err := proposeMix(selection, digest, diskMiB)
	if err != nil {
		return MixProposal{}, err
	}
	if ctx.Err() != nil {
		return MixProposal{}, ctx.Err()
	}
	return p, nil
}
func proposeMix(selection Selection, digest string, diskMiB int64) (MixProposal, error) {
	cpu, ram := selection.Partition.Guests.CPUMilli, selection.Partition.Guests.MemoryBytes/MiB
	p := MixProposal{Host: selection.Binding.Host, SelectionDigest: digest, SelectionExpiresAt: selection.ExpiresAt, GuestCPU: cpu, GuestRAM: ram, State: "proposed_from_saved_configuration_not_applied", Entries: []MixSize{}}
	for _, size := range []MixSize{{Name: "small", CPU: 1000, RAM: 2048}, {Name: "medium", CPU: 2000, RAM: 4096}, {Name: "heavy", CPU: 4000, RAM: 8192}} {
		if size.CPU <= cpu && size.RAM <= ram {
			size.Disk = diskMiB
			p.Entries = append(p.Entries, size)
		}
	}
	if len(p.Entries) == 0 {
		return MixProposal{}, ErrSelection
	}
	// Include one larger size when doing so still leaves room for a small runner;
	// fill the remainder with small runners. This is an editable starter mix, not
	// a claim about measured performance, disk fit or actual concurrent execution.
	for i := len(p.Entries) - 1; i > 0; i-- {
		e := &p.Entries[i]
		if cpu-e.CPU >= 1000 && ram-e.RAM >= 2048 {
			e.Count = 1
			cpu -= e.CPU
			ram -= e.RAM
		}
	}
	count := min(cpu/1000, ram/2048)
	if count > 1000000 {
		return MixProposal{}, ErrSelection
	}
	p.Entries[0].Count = count
	return p, nil
}
