//go:build linux

package nativecontrol

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeretained"
	"net/http"
	"strings"
	"time"
)

// This is a diagnostic join of actual retained observations, never canonical
// physical completion. It does not change unknown history or release charges.
func (o *executionOwner) serveRetainedEvidence(w http.ResponseWriter, r *http.Request, serverLeaf, ownerLeaf string) bool {
	if !strings.HasPrefix(r.URL.Path, nativeretained.PathPrefix) || !strings.HasSuffix(r.URL.Path, "/evidence") {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, nativeretained.PathPrefix), "/evidence")
	nonce := r.URL.Query().Get("nonce")
	if r.Method != http.MethodGet || !hash(id) || !hash(nonce) || r.URL.RawPath != "" || r.URL.RawQuery != "nonce="+nonce {
		http.Error(w, "exact retained-evidence nonce GET required", 400)
		return true
	}
	began := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if o == nil || o.api == nil {
		http.Error(w, "retained observations unavailable; charges remain", 409)
		return true
	}
	o.mu.Lock()
	x := o.retirementSteps[id]
	o.mu.Unlock()
	if x == nil {
		http.NotFound(w, r)
		return true
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.retirement == nil || x.retirement.deleted == nil || x.retirement.deleted.claim.ID != id || x.retirement.deleted.current.Outcome != "succeeded" || !x.reclamationCaptured || !retirementMatches(x, x.retirement.intent) {
		http.Error(w, "original retained DELETE evidence unavailable; charges remain", 409)
		return true
	}
	step := x.retirement.deleted
	evidence, e := o.api.ObserveRetirementEvidence(ctx, x.prepared, step.operation, x.guest, x.reclamation)
	if e != nil || !evidence.Fresh() {
		http.Error(w, "retained observation join unavailable; charges remain", 409)
		return true
	}
	i, e := o.api.Identity(ctx)
	if e != nil || !i.Valid() || o.api.CheckBoundary(ctx) != nil || ctx.Err() != nil || time.Since(began) > 5*time.Second || !evidence.Fresh() {
		http.Error(w, "retained identity changed or expired; charges remain", 409)
		return true
	}
	namespace, absent, storage := evidence.Namespace(), evidence.InstanceAbsence(), evidence.Storage()
	deleted := evidence.Deletion()
	if step.current.ClaimID != step.claim.ID || step.current.RequestDigest != step.claim.RequestDigest || step.current.IntentDigest != step.claim.IntentDigest || step.current.PermitID != x.retirement.intent.PermitID || deleted.OperationID() != step.current.OperationID || deleted.ResourceID() != step.current.ResourceID || deleted.Dataset() != step.current.Dataset || deleted.DatasetGUID() != step.current.DatasetGUID || namespace.DomainID() != step.current.DomainID || namespace.PolicyDigest() != step.current.PolicyDigest || namespace.Name() != step.current.NativeName || storage.DomainID() != step.current.DomainID || storage.PolicyDigest() != step.current.PolicyDigest || storage.Name() != step.current.NativeName || i.DomainID() != step.current.DomainID || o.api.PolicyDigest() != step.current.PolicyDigest {
		http.Error(w, "retained identities differ; charges remain", 409)
		return true
	}
	root, pool := storage.Accounting().RootAccounting(), storage.Accounting().PoolAccounting()
	d := nativeretained.Descriptor{Version: nativeretained.Version, Nonce: nonce, IssuedAt: began.UTC(), Owner: nativeidentity.Descriptor{DomainID: i.DomainID(), PolicyDigest: o.api.PolicyDigest(), OwnerClientLeaf: ownerLeaf, OwnerServerLeaf: serverLeaf, NativeServerKey: i.ServerKey(), NativeOrigin: i.Origin(), NativeScope: i.Scope(), NativeServerName: i.ServerName(), NativeServerVersion: i.ServerVersion()}, Delete: step.current, Qualification: "retained_observations_only", ReleaseAuthorized: false, NamespaceInode: namespace.NamespaceInode(), NamespaceTerminated: namespace.Terminated(), NamespaceObservedAt: namespace.ObservedAt(), InstanceAbsent: absent.Valid(), InstanceObservedAt: absent.ObservedAt(), StorageObservedAt: storage.ObservedAt(), RootGUID: root.GUID(), RootQuotaBytes: root.Quota(), RootUsedBytes: root.Used(), RootAvailableBytes: root.Available(), PoolGUID: pool.GUID(), PoolLoadGUID: pool.LoadGUID(), PoolAllocatedBytes: pool.Allocated(), PoolFreeBytes: pool.Free(), PoolSizeBytes: pool.Size()}
	if nativeretained.ValidateDescriptor(d) != nil || !evidence.Fresh() {
		http.Error(w, "retained descriptor unavailable; charges remain", 409)
		return true
	}
	respond(w, 200, d)
	return true
}
