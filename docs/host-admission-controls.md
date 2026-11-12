# Durable host admission controls

Status: trusted administrative **ledger primitive**, not a new public/provider endpoint or a complete job-safe drain workflow. It does not call Garm, GitHub or Incus.

Within this module, an authenticated administrative boundary can call:

```go
err := store.SetHostMode(ctx, "host-a", "draining")
// Also supported: "disabled" and "enabled".
```

`SetHostMode` changes only a known host's mode. It never changes budgets, host placement, allocations, runner states, credentials or existing resources. Unknown targets/modes are rejected. Policy and target/mode audit are committed in one `BEGIN IMMEDIATE` transaction; an audit failure rolls back the entire change.

Every **changed** mode also durably closes global readiness and invalidates an active collector nonce. A pre-change collector cannot reopen admission. Enabling a host therefore needs a NEW barrier and a fresh complete authoritative Garm/Incus/operation inventory, not the last previously healthy view. Normal reconciliation preserves the administrator's mode. A true no-op/retry neither reopens readiness nor invalidates a currently active collector unnecessarily.

This conservative global barrier can briefly pause other hosts while the next complete reconciliation runs. It is not a distributed remote fencing mechanism. A permit committed before the mode change is already in flight and may complete afterwards. Its allocation remains charged and its outcome remains recordable; replay can never obtain a second permit. A race between a new claim and a mode change is serialized by SQLite and visible in audit order.

## What “draining” does and does not mean

In this primitive, `draining` means **new provisioning has been paused for this host**. Existing idle runners can still accept GitHub jobs. Running work is not canceled, runners are not deregistered and instances are not deleted. There is no `drain_complete` assertion or timeout-based release. Disabling likewise does not power off or remove guests.

Existing creating, idle, running, failed, deleting and unknown allocations remain charged. A web UI must not turn this primitive into a misleading “drained” badge or claim job eligibility stopped. Until runner deregistration/job-assignment races and authoritative retirement are qualified, label the operator effect as “pause new runners” / “drain requested, existing runners still eligible,” not complete draining.

The Garm provider's mTLS RPC deliberately has no host-mode/policy route. A future separate admin API must add reviewed authentication, authorization, attributable administrator identity and CSRF/session handling as appropriate before exposing this method. The primitive records host/mode target events, not a complete authenticated admin identity/retention system.

## Tested invariants

- Every resource/reservation and its conservative charge survives drain/disable/enable unchanged.
- An old receipt cannot dispatch on a draining host, and enable cannot bypass fresh reconciliation.
- A pre-change collector fence is consumed; no-op retry preserves a valid collector.
- Mode and audit roll back together on audit failure.
- Cross-handle claim/mode races permit a claim only when its durable audit precedes the mode change.
- Already committed work stays in flight/charged and never gets a replay permit.

No live system was accessed to test these controls. Full retirement/draining, installed physical-scope qualification and production admin UI remain explicit gates.
