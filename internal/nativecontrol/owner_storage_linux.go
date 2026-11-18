//go:build linux

package nativecontrol

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"time"
)

// This authenticated diagnostic is not a completion or release receipt. It
// deliberately has no released/freed-byte field. Full-pool names stay private.
type storageAccountingReport struct {
	Version            string    `json:"version"`
	State              string    `json:"state"`
	DomainID           string    `json:"domain_id"`
	PolicyDigest       string    `json:"policy_digest"`
	ResourceID         string    `json:"resource_id"`
	ObservedAt         time.Time `json:"observed_at"`
	RootGUID           string    `json:"root_guid"`
	RootQuotaBytes     uint64    `json:"root_quota_bytes"`
	RootUsedBytes      uint64    `json:"root_used_bytes"`
	RootAvailableBytes uint64    `json:"root_available_bytes"`
	PoolGUID           string    `json:"pool_guid"`
	PoolLoadGUID       string    `json:"pool_load_guid"`
	PoolAllocatedBytes uint64    `json:"pool_allocated_bytes"`
	PoolFreeBytes      uint64    `json:"pool_free_bytes"`
	PoolSizeBytes      uint64    `json:"pool_size_bytes"`
}

// Caller holds x.mu. Reads may be retried against the ORIGINAL before-START
// tracker, never against a reconstructed or newly captured reference baseline.
func (o *executionOwner) retirementStorageAccounting(ctx context.Context, x *ownerExecution, s *ownerRetirementStep) (storageAccountingReport, error) {
	if o == nil || o.api == nil || o.pools == nil || x == nil || x.retirement == nil || s == nil || s != x.retirement.deleted || s.current.Outcome != "succeeded" || !x.reclamationCaptured {
		return storageAccountingReport{}, ErrControl
	}
	x.retirement.accounting = nil
	observed, e := o.api.ObserveStorageReclamation(ctx, x.prepared, x.reclamation)
	if e != nil {
		return storageAccountingReport{}, ErrControl
	}
	report, e := storageReport(observed, x.retirement.intent.DomainID, x.retirement.intent.PolicyDigest, x.retirement.intent.ResourceID)
	if e != nil {
		return storageAccountingReport{}, e
	}
	x.retirement.accounting = &observed
	return report, nil
}
func storageReport(o nativeapi.StorageReclamationObservation, domain, policy, resource string) (storageAccountingReport, error) {
	a := o.Accounting()
	if !a.Fresh() || o.DomainID() != domain || o.PolicyDigest() != policy || o.ResourceID() != resource {
		return storageAccountingReport{}, ErrControl
	}
	root, pool := a.RootAccounting(), a.PoolAccounting()
	if root.GUID() == "" || pool.GUID() == "" || pool.LoadGUID() == "" || pool.Freeing() != 0 {
		return storageAccountingReport{}, ErrControl
	}
	return storageAccountingReport{Version: "native-storage-accounting/v1", State: "accounting_only_not_release", DomainID: domain, PolicyDigest: policy, ResourceID: resource, ObservedAt: o.ObservedAt(), RootGUID: root.GUID(), RootQuotaBytes: root.Quota(), RootUsedBytes: root.Used(), RootAvailableBytes: root.Available(), PoolGUID: pool.GUID(), PoolLoadGUID: pool.LoadGUID(), PoolAllocatedBytes: pool.Allocated(), PoolFreeBytes: pool.Free(), PoolSizeBytes: pool.Size()}, nil
}
