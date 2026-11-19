//go:build linux

package managedgarm

import (
	"context"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeenforcement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/profilesnapshot"
)

// PoolCapacityObservation joins actual loaded stock configuration, its pinned
// ledger source and separately authenticated native enforcement. It is not a
// grant, desired runner count, or authorization to bypass the journaled writer.
// No caller-provided budget or maximum is accepted.
type PoolCapacityObservation struct {
	binding                         *LoadedStockBinding
	pool                            string
	sequence                        int64
	profileDigest, accountingDigest string
	budget, charged                 admission.Resources
	observations                    []nativeenforcement.Observation
	at                              time.Time
}

func (o PoolCapacityObservation) Valid() bool {
	age := time.Since(o.at)
	if o.binding == nil || o.at.IsZero() || age < 0 || age > 5*time.Second || len(o.observations) == 0 {
		return false
	}
	for _, observed := range o.observations {
		if !observed.Valid() {
			return false
		}
	}
	return true
}

// Check refuses any ledger revision change rather than silently substituting a
// new plan. A writer must call it at its own dispatch boundary; an observation
// alone cannot close a subsequent distributed mutation race.
func (o PoolCapacityObservation) Check(ctx context.Context) error {
	if !o.Valid() || o.binding.Check(ctx) != nil {
		return ErrBoundary
	}
	s, err := o.binding.observer.profiles.read(ctx)
	if err != nil || s.Sequence != o.sequence || s.ProfileDigest != o.profileDigest || s.AccountingDigest != o.accountingDigest {
		return ErrBoundary
	}
	if validatePoolCapacityJoin(s, o.pool, o.observations) != nil || !o.Valid() {
		return ErrBoundary
	}
	return nil
}

// ObservePoolCapacity only succeeds with an actual controlled startup binding
// and real, unexpired enforcement observations for every enrolled pool domain.
func (b *LoadedStockBinding) ObservePoolCapacity(ctx context.Context, pool string, observed []nativeenforcement.Observation) (PoolCapacityObservation, error) {
	began := time.Now()
	if len(observed) == 0 || len(observed) > 1000 {
		return PoolCapacityObservation{}, ErrBoundary
	}
	if b.Check(ctx) != nil {
		return PoolCapacityObservation{}, ErrBoundary
	}
	s, err := b.observer.profiles.read(ctx)
	owned := append([]nativeenforcement.Observation(nil), observed...)
	if err != nil || validatePoolCapacityJoin(s, pool, owned) != nil || b.Check(ctx) != nil {
		return PoolCapacityObservation{}, ErrBoundary
	}
	o := PoolCapacityObservation{binding: b, pool: pool, sequence: s.Sequence, profileDigest: s.ProfileDigest, accountingDigest: s.AccountingDigest, budget: s.Accounting.Budgets.Pools[pool], charged: s.Accounting.Allocated.Pools[pool], observations: owned, at: began}
	if !o.Valid() {
		return PoolCapacityObservation{}, ErrBoundary
	}
	return o, nil
}

func validatePoolCapacityJoin(s profilesnapshot.Snapshot, pool string, observed []nativeenforcement.Observation) error {
	p, ok := s.Payload.Profiles[pool]
	if !ok || !s.Accounting.LedgerReady || len(p.Domains) == 0 || len(p.Domains) > 1000 || len(observed) != len(p.Domains) || len(s.Payload.PoolHosts[pool]) != len(p.Domains) {
		return ErrBoundary
	}
	if !resourcesWithin(s.Accounting.Allocated.Global, s.Accounting.Budgets.Global) || !resourcesWithin(s.Accounting.Allocated.Pools[pool], s.Accounting.Budgets.Pools[pool]) {
		return ErrBoundary
	}
	records := map[string]nativeenforcement.Descriptor{}
	for _, o := range observed {
		if !o.Valid() {
			return ErrBoundary
		}
		d := o.Record()
		if _, exists := records[d.Owner.DomainID]; exists {
			return ErrBoundary
		}
		records[d.Owner.DomainID] = d
	}
	// Persisted overhead is an additional canonical charge, never permission to
	// reclaim old reservations. Require the exact enrolled birth and enforcement
	// commitments rather than accepting a subtotal from another owner/domain.
	overheads := map[int64]admission.NativeDomainOverhead{}
	for _, overhead := range s.Accounting.DomainOverheads {
		if admission.ValidateNativeDomainOverhead(overhead) != nil || overhead.Sequence > s.Sequence {
			return ErrBoundary
		}
		if _, duplicate := overheads[overhead.DomainBindingSequence]; duplicate {
			return ErrBoundary
		}
		overheads[overhead.DomainBindingSequence] = overhead
	}
	seenHosts := map[string]bool{}
	for _, ref := range p.Domains {
		enrollment, exists := s.Payload.NativeDomains[ref.Sequence]
		d, found := records[ref.DomainID]
		hostBudget, hasHost := s.Accounting.Budgets.Hosts[ref.Host]
		if !exists || !found || !hasHost || seenHosts[ref.Host] || enrollment.Sequence != ref.Sequence || enrollment.Spec.Controller != s.Controller || enrollment.Spec.Host != ref.Host || enrollment.Spec.HostBindingSequence != ref.HostBindingSequence || s.Payload.HostBindings[ref.Host] != ref.HostBindingSequence || enrollment.Spec.DomainID != ref.DomainID || enrollment.Spec.PolicyDigest != ref.PolicyDigest || d.Owner.DomainID != ref.DomainID || d.Owner.PolicyDigest != ref.PolicyDigest || d.Owner.OwnerClientLeaf != enrollment.Spec.OwnerClientLeaf || d.Owner.OwnerServerLeaf != enrollment.Spec.OwnerServerLeaf || d.CapsuleBirthID != ref.DomainID {
			return ErrBoundary
		}
		overhead, retained := overheads[ref.Sequence]
		if !retained || overhead.Host != ref.Host || overhead.DomainID != ref.DomainID || overhead.Evidence.Owner != d.Owner || overhead.Evidence.RootImageSHA256 != d.RootImageSHA256 || overhead.Evidence.DeviceProgramSHA256 != d.DeviceProgramSHA256 || overhead.Evidence.EgressPolicySHA256 != d.EgressPolicySHA256 || !overheadCoversEnforcement(overhead.Resources, d.Limits) {
			return ErrBoundary
		}
		member := false
		for _, h := range s.Payload.PoolHosts[pool] {
			if h == ref.Host {
				member = true
			}
		}
		if !member {
			return ErrBoundary
		}
		if !resourcesWithin(s.Accounting.Allocated.Hosts[ref.Host], hostBudget) || !budgetFitsEnforcement(hostBudget, d.Limits) {
			return ErrBoundary
		}
		seenHosts[ref.Host] = true
	}
	return nil
}
func resourcesWithin(a, b admission.Resources) bool {
	return a.CPU >= 0 && a.RAM >= 0 && a.Disk >= 0 && a.Runners >= 0 && b.CPU > 0 && b.RAM > 0 && b.Disk > 0 && b.Runners > 0 && a.CPU <= b.CPU && a.RAM <= b.RAM && a.Disk <= b.Disk && a.Runners <= b.Runners
}
func budgetFitsEnforcement(b admission.Resources, l nativeenforcement.Limits) bool {
	// Divide the actual byte limits, avoiding overflow and rounding capacity down.
	return b.CPU > 0 && b.RAM > 0 && b.Disk > 0 && b.Runners > 0 && l.Tasks > 0 && uint64(b.CPU) <= l.CPUMilli && uint64(b.RAM) <= l.MemoryBytes/(1<<20) && uint64(b.Disk) <= l.DiskBytes/(1<<20)
}

// Round consumption up, unlike capacity which is rounded down. Retained records
// may exceed a tightened current ceiling; a newly raised ceiling needs a new
// canonical overhead record before this observation can succeed.
func overheadCoversEnforcement(r admission.Resources, l nativeenforcement.Limits) bool {
	if _, err := nativeenforcement.DigestLimits(l); err != nil {
		return false
	}
	const mib = uint64(1 << 20)
	return r.Runners == 0 && r.CPU > 0 && r.RAM > 0 && r.Disk > 0 && uint64(r.CPU) >= l.CPUMilli && uint64(r.RAM) >= (l.MemoryBytes+mib-1)/mib && uint64(r.Disk) >= (l.DiskBytes+mib-1)/mib
}
