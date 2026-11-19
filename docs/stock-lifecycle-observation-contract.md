# Stock lifecycle observations are not a cleanup frontier

Read-only source audit for a controller that changes stock GARM pool settings
while GARM retains the complete runner lifecycle. This document adds no runtime
API, mutation, permission, reservation binding, cleanup retry or release path.
Incus `dir` remains a valid candidate for that architecture; ZFS is not a new
requirement for a logical runner-count policy.

## Exact source and scope

All stock statements below were checked with `git show` at upstream GARM
`154638445c3949c1958b01812f69d9a1e4d82684`, not the modified checkout HEAD.
`runner/pool/pool.go` has Git blob ID
`9f5fc197272d7aabd04351e3dfd7bd2583a505a6`. This establishes source behavior,
not the version or behavior of an installed server. Scale sets and other provider
implementations are outside this audit.

## Accepted provisioning work

In that exact `runner/pool/pool.go`:

- Lines 1326–1344 check enabled/count/max before calling `AddRunner`.
- Lines 936–997 fetch the pool, allocate JIT configuration when applicable, then
  create the `pending_create` database row. JIT acquisition precedes that row.
- Lines 1709–1780 select pending rows, mark them `creating`, then launch provider
  work asynchronously. This loop does not recheck enabled/max. The called
  `addInstanceToProvider` (1039 onward) reloads pool configuration but does not
  repeat the enabled/count/max admission check before its provider call at 1131.
- Lines 1400–1487 retry eligible failed instances: after provider cleanup returns
  successfully, they become `pending_create` again. The retry path checks pool
  enabled and attempt limits; an error row is not a terminal creation frontier.

**Inference:** a completed settings reduction does not attest that all work which
passed an earlier decision has finished. A row snapshot also omits any pre-row
JIT interval. No duration or number of matching reads closes this gap. These are
ordering observations, not an exploitation or concurrency reproduction.

## What cleanup observations establish

| Stock observation | Strictly supported conclusion | Not established |
| --- | --- | --- |
| `pending_create` / `creating` row | Recorded provisioning work remains | No future effects after a settings reduction |
| `error` row | A create attempt reported error | Terminal failure of every external effect, or no retry |
| `pending_delete` / `deleting` row | Cleanup is requested/in progress in stock state | Completed provider cleanup |
| Provider Delete returns nil | Provider interface reported success | Exact Incus operation/incarnation history or freed filesystem bytes |
| Stock row disappears | Row is no longer returned by this snapshot | Which cleanup path occurred, physical deletion, or accepted-create frontier |

The last distinction is material:

1. `deletePendingInstances`, lines 1679–1697, normally retains the row on provider
   error, but the `pending_force_delete` path proceeds to database deletion even
   when the provider reports an error.
2. Reconciliation, lines 828–853, can remove the database row after provider-list
   absence and GitHub deregistration handling. It does not retain an exact native
   deletion operation in the returned row snapshot.
3. The v0.1.1 external provider adapter, `runner/providers/v0.1.1/external.go`,
   lines 155–166, accepts the provider's designated not-found exit as successful
   deletion. Interface success therefore does not necessarily mean a new delete
   operation ran. A provider's actual implementation requires separate review.

These stock behaviors can be appropriate for stock lifecycle reconciliation.
They are not additional physical-release evidence for this controller.

## Minimal count-budget consequence

Retain busy, pending, deleting, error and unknown work conservatively when planning
transfers. Lower donor maxima first; a confirmed lower setting is not a freed
slot. Partial or uncertain settings writes stop the transfer. Stock UI/CLI edits
require conflict handling and complete settings-writer scope.

A settings-only controller can expose a dynamic **soft count target** without
claiming an atomic global admission ceiling. A strict never-exceed count requires
a demonstrated accepted-work boundary, not a larger polling delay. This audit
neither implements nor grants such a boundary. No request-to-reservation binding
or replacement mechanism is proposed here.

Do not reuse the existing multidimensional ledger release operation for a
count-only policy: it also removes resource charges. Logical count planning does
not need to claim per-guest reclaimed disk bytes; physical disk guarantees would
need actual filesystem/provider evidence and remain separate.

## Verification and next evidence

Existing `incusapi` observation tests already enforce that scoped reads retain
explicit blockers and cannot authorize release; duplicating them with another
synthetic "completed" boolean would add no stock evidence. This slice is a source
contract only, with no new executable production classifier or positive permit.

Before relying on a particular installation, establish its exact GARM/provider
source identities, effective pool/provider scope, force-removal behavior and
complete settings-writer ownership. Qualify the original provider operation and
cleanup semantics separately if physical completion is required. Installed-host
inspection and disposable runtime qualification require their own approval;
none was performed for this audit.
