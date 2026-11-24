package reuse

import (
	"sort"
)

// Verdict describes what the synthetic matcher classified as a candidate.
// Held reasons explain unqualified components; neither field changes any
// accounting or establishes the authenticity of an evidence record.
type Verdict struct {
	Charge    ChargeIdentity `json:"charge"`
	Candidate Dimensions     `json:"candidate"`
	Held      []string       `json:"held"`
	Digest    string         `json:"digest"`
}

// mergedBundle is the conflict-checked union of matching evidence deliveries.
type mergedBundle struct {
	workload *WorkloadProof
	teardown *InstanceTeardownProof
	cleanup  *CleanupProof
	volumes  map[string]VolumeAbsence
}

func (b *mergedBundle) conflictingTeardown(next *InstanceTeardownProof) bool {
	if b.teardown == nil {
		return false
	}
	return b.teardown.Delete.OperationID != next.Delete.OperationID ||
		b.teardown.Identity.SubjectFingerprint != next.Identity.SubjectFingerprint
}

func (b *mergedBundle) conflictingWorkload(next *WorkloadProof) bool {
	if b.workload == nil {
		return false
	}
	return b.workload.JobID != next.JobID || b.workload.RunID != next.RunID ||
		b.workload.RunAttempt != next.RunAttempt || b.workload.RunnerID != next.RunnerID
}

// Assess merges every delivered bundle deterministically: deliveries are
// sorted by local receipt time and digest, duplicates collapse, partial
// deliveries union, and any contradiction poisons the whole charge with
// conflicting_evidence. Evidence for a distinct charge — including a
// same-name replacement — never contributes to this charge.
func Assess(charge ChargeIdentity, delivered []TerminalEvidence) Verdict {
	verdict := Verdict{Charge: charge, Held: []string{}}
	if !charge.valid() {
		verdict.Held = append(verdict.Held, HeldInvalidCharge)
		return verdict
	}
	matching := []TerminalEvidence{}
	rejected := map[string]bool{}
	for _, evidence := range delivered {
		if evidence.Valid() && evidence.matches(charge) {
			matching = append(matching, evidence)
			continue
		}
		if evidence.Charge.valid() && DisplayNameReused(charge, evidence.Charge) {
			rejected[HeldSameNameReplacement] = true
		} else {
			rejected[HeldEvidenceForDistinctCharge] = true
		}
	}
	sort.Slice(matching, func(i, j int) bool {
		a, b := matching[i], matching[j]
		if !a.CapturedAt.Equal(b.CapturedAt) {
			return a.CapturedAt.Before(b.CapturedAt)
		}
		return a.Digest() < b.Digest()
	})
	bundle := mergedBundle{volumes: map[string]VolumeAbsence{}}
	conflict := false
	for _, evidence := range matching {
		workload, teardown, cleanup := evidence.components()
		if workload {
			if bundle.conflictingWorkload(evidence.Workload) {
				conflict = true
			} else if bundle.workload == nil {
				bundle.workload = evidence.Workload
			}
		}
		if teardown {
			if bundle.conflictingTeardown(evidence.Teardown) {
				conflict = true
			} else if bundle.teardown == nil {
				bundle.teardown = evidence.Teardown
			}
		}
		if cleanup {
			if bundle.cleanup == nil {
				bundle.cleanup = evidence.Cleanup
			}
			for _, volume := range evidence.Cleanup.Volumes {
				if previous, exists := bundle.volumes[volume.Name]; exists && previous.ScopeDigest != volume.ScopeDigest {
					conflict = true
				}
				bundle.volumes[volume.Name] = volume
			}
		}
	}
	if bundle.workload != nil && bundle.teardown != nil &&
		bundle.workload.CompletedAt.After(bundle.teardown.Delete.CapturedAt) {
		conflict = true
		rejected[HeldChronologyConflict] = true
	}
	if conflict {
		rejected[HeldConflictingEvidence] = true
	}
	verdict.Candidate, verdict.Held = qualify(charge, bundle)
	if conflict {
		verdict.Candidate = Dimensions{}
	}
	for reason := range rejected {
		verdict.Held = append(verdict.Held, reason)
	}
	verdict.Held = sortedUnique(verdict.Held)
	verdict.Digest = canonicalDigest(struct {
		Charge   ChargeIdentity
		Workload *WorkloadProof
		Teardown *InstanceTeardownProof
		Cleanup  *CleanupProof
	}{charge, bundle.workload, bundle.teardown, bundle.cleanup})
	return verdict
}

// qualify models compute and disk independently. Compute requires the
// completed-workload and teardown components; disk requires applicable
// cleanup, a modeled capability, or no originally charged disk.
func qualify(charge ChargeIdentity, bundle mergedBundle) (Dimensions, []string) {
	held := []string{}
	workload := bundle.workload != nil
	teardown := bundle.teardown != nil
	computing := workload && teardown
	if !workload {
		held = append(held, HeldAwaitingCompletedWorkload)
	}
	if !teardown {
		held = append(held, HeldAwaitingTerminalDelete, HeldAwaitingPostDeleteAbsence)
	}
	disk := charge.Ref.Disk == 0
	if !disk && bundle.cleanup != nil {
		volumesVerified := true
		for _, volume := range charge.Volumes {
			if _, absent := bundle.volumes[volume.Name]; !absent {
				volumesVerified = false
			}
		}
		capability := bundle.cleanup.Capability != nil
		disk = volumesVerified || capability
	}
	if !disk {
		held = append(held, HeldResidualVolumeUnverified)
	}
	return Dimensions{CPU: computing, RAM: computing, Runners: computing, Disk: disk}, held
}

func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	out := values[:0]
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
