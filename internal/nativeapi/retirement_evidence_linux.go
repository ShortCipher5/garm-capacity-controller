//go:build linux

package nativeapi

import (
	"context"
	"time"
)

// RetirementEvidence is a joined observation of the original retained resource,
// not physical release authority. In particular, historical shared extents and
// host-global kernel/cache memory are not attributed by these observations.
// It cannot be reconstructed from a descriptor or substituted with an API404.
type RetirementEvidence struct {
	absent    DeletedInstanceObservation
	deleted   RetirementOperation
	namespace PIDNamespaceTerminationObservation
	storage   StorageReclamationObservation
	began     time.Time
}

func (o RetirementEvidence) Fresh() bool {
	age := time.Since(o.began)
	return !o.began.IsZero() && age >= 0 && age <= 5*time.Second && o.absent.Valid() && o.absent.ResourceID() == o.deleted.ResourceID() && o.deleted.Valid() && o.deleted.Step() == "delete" && o.deleted.Outcome() == "succeeded" && o.namespace.Terminated() && o.storage.Accounting().Fresh() && o.namespace.ResourceID() == o.deleted.ResourceID() && o.storage.ResourceID() == o.deleted.ResourceID() && o.namespace.DomainID() == o.storage.DomainID() && o.namespace.PolicyDigest() == o.storage.PolicyDigest() && o.namespace.Name() == o.storage.Name()
}
func (o RetirementEvidence) InstanceAbsence() DeletedInstanceObservation {
	if !o.Fresh() {
		return DeletedInstanceObservation{}
	}
	return o.absent
}
func (o RetirementEvidence) ObservedAt() time.Time {
	if !o.Fresh() {
		return time.Time{}
	}
	return o.began.UTC()
}
func (o RetirementEvidence) Namespace() PIDNamespaceTerminationObservation {
	if !o.Fresh() {
		return PIDNamespaceTerminationObservation{}
	}
	return o.namespace
}
func (o RetirementEvidence) Storage() StorageReclamationObservation {
	if !o.Fresh() {
		return StorageReclamationObservation{}
	}
	return o.storage
}
func (o RetirementEvidence) Deletion() RetirementOperation {
	if !o.Fresh() {
		return RetirementOperation{}
	}
	return o.deleted
}

// ObserveRetirementEvidence performs only reads. The exact acknowledged native
// DELETE, retained actual PID1 namespace and original before-START pool-reference
// baseline must all still be available. A missing handle or any uncertainty
// fails closed, with no reconstruction, mutation retry or charge adjustment.
func (c *Client) ObserveRetirementEvidence(ctx context.Context, p Prepared, deleted RetirementOperation, guest GuestIncarnation, tracker StorageReclamationTracker) (RetirementEvidence, error) {
	if ctx == nil || ctx.Err() != nil || c == nil || c.capsule == nil || !c.matches(p) || !deleted.Valid() || deleted.Step() != "delete" || deleted.Outcome() != "succeeded" || guest.client != c || tracker.client != c || guest.resource != deleted.ResourceID() || tracker.resource != deleted.ResourceID() || guest.name != p.name || tracker.name != p.name || guest.policy != c.PolicyDigest() || tracker.policy != c.PolicyDigest() || tracker.domain != c.policy.DomainID() {
		return RetirementEvidence{}, ErrNative
	}
	began := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if c.CheckBoundary(ctx) != nil {
		return RetirementEvidence{}, ErrNative
	}
	before, e := c.ObservePIDNamespaceTermination(ctx, p, guest)
	if e != nil || !before.Terminated() {
		return RetirementEvidence{}, ErrNative
	}
	absent, e := c.ObserveDeletedInstance(ctx, p, deleted)
	if e != nil || !absent.Valid() || absent.ResourceID() != deleted.ResourceID() {
		return RetirementEvidence{}, ErrNative
	}
	storage, e := c.ObserveStorageReclamation(ctx, p, tracker)
	if e != nil || !storage.Accounting().Fresh() {
		return RetirementEvidence{}, ErrNative
	}
	after, e := c.ObservePIDNamespaceTermination(ctx, p, guest)
	if e != nil || !after.Terminated() || after.NamespaceInode() != before.NamespaceInode() {
		return RetirementEvidence{}, ErrNative
	}
	// Do not reuse the earlier name-absence sample across storage work.
	absent, e = c.ObserveDeletedInstance(ctx, p, deleted)
	if e != nil || !absent.Valid() || absent.ResourceID() != deleted.ResourceID() || c.CheckBoundary(ctx) != nil || ctx.Err() != nil {
		return RetirementEvidence{}, ErrNative
	}
	out := RetirementEvidence{absent: absent, deleted: deleted, namespace: after, storage: storage, began: began}
	if !out.Fresh() {
		return RetirementEvidence{}, ErrNative
	}
	return out, nil
}
