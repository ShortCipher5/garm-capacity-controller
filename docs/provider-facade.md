# Policy-bound provider facade and reconciliation

Status: server-side implementation, **not an exposed provider executable or qualified live scheduling service**. No infrastructure is accessed by tests. Admin authentication, isolated gateway transport and full lifecycle retirement are not enabled by this library.

## Implemented boundary

`internal/gateway` binds the SQLite ledger to `incusapi.Client` through a deliberately narrow backend interface. Trusted configuration fixes a pool's host, whole-vCPU/RAM allocation, Incus type, profiles, locally cached image fingerprint, architecture and Garm flavor/image identifiers. Constructor copies configuration and verifies host/pool compatibility with the ledger. No request-provided placement or arbitrary Incus options can override it. Initial placement is fixed, not a priority scheduler or automatic host chooser.

1. The opt-in pre-JIT admission request is validated and mapped to a stable SHA-256 key over controller, pool and runner name. `Admit` emits the strict reserved or deferred protocol; it never calls Incus.
2. `Create` requires that exact reservation and matching Garm policy. It uses pinned `garm-provider-common` v0.1.9 for Linux amd64/arm64 cloud-init generation. Bootstrap/debug/custom extraspec overrides are rejected in this initial path. Rendering performs no download. This raises the integrated branch's Go minimum to 1.25; the separate actual pinned Garm harness requires Garm's newer toolchain.
3. Durable `ExecuteDispatch` owns one concrete `Create` call. Its validated UUID/name is committed as `submitted` through the same opaque permit BEFORE `Wait`. Afterwards an exact Incus instance read verifies name, controller/pool/reservation labels, CPU/RAM and running state before confirmation. Failed persistence, wait or changed readback is unknown and never redispatches. See [the pre-wait journal and crash boundary](submitted-correlation.md).
4. Confirmed replay can read back and return the same verified instance. It is never a new create permit. Unknown or intent replay returns a generic unavailable error. Bootstrap userdata, callback tokens and provider fault bodies are not stored in SQLite or returned in errors.

The Garm caller is a **trusted authenticated controller**, not repository workflow code or the browser. Bootstrap contains privileged guest provisioning information. The [experimental gateway service](gateway-service.md) wraps this library with scoped mTLS, bounded requests and separate credential ownership. Separate OS identities and protected private files remain operator requirements. The library alone is not that network boundary.

## Reconciliation

`Facade.Reconcile` persists `BeginReconciliation` before reading any source. Then it collects fresh Garm rows, every configured Incus instance collection and operation collection, before consuming the exact fence with `CompleteReconciliation`.

- Garm creating/failed/unknown/deleting rows remain charged. Names and provider IDs must match the gateway's fixed identity scheme. Unknown pool/host placement is not guessed. Busy/active or job-bearing runners are not described as idle.
- Garm and Incus records sharing host/name merge using componentwise maximum allocation and only an exact existing reservation link. Post-backup tagged resources without reservations import as charged orphans. Missing resources remain unknown and charged. Immutable-link conflicts block reconciliation.
- Unowned or unmapped physical allocations, changed correlation labels, nonpositive/unbounded limits, failed reads, incomplete placement and active/unknown operations leave the durable barrier closed. A terminal operation referring to an invisible instance also blocks. No timeout or missing entry releases capacity.
- Provider running is not proof that a GitHub runner is idle. The implementation does not deregister idle runners, cancel jobs, delete instances, stop/start guests or release reservations.

`internal/garmapi` implements the actual read-only HTTPS `GET /api/v1/instances` collection from pinned Garm v0.2.1. It uses explicit CA/hostname verification and an isolated bearer token, refuses redirects/proxies, bounds responses, rejects duplicate/malformed critical rows, and scopes records to configured provider names. Scaleset IDs require explicit mapping to the pseudo-pool identity used by the pre-JIT patch. It retains only allocation identity/state, not job details or fault messages. A failed/unauthorized response is never an empty complete inventory.

## Remaining deployment gates

A project-scoped collection alone is not proof of a complete physical-host inventory. Host/project exclusivity, all competing provisioning paths, native Incus isolation, system headroom, immutable policy and external administrator privileges must be qualified before enabling scheduling. An imported physical allocation larger than its policy is charged at the larger size, not silently shrunk.

The consistent backup/restore APIs in [the recovery slice](backup-restore.md) keep restored copies blocked. Old gateway writers must be stopped/fenced and fresh authoritative physical inventory collected before a restored ledger is used. This library does not revoke external credentials or stop a process still using an old database.

The experimental credential-isolated gateway/executable is implemented in the next stacked slice. Remaining gates: installed-provider/Incus qualification; authoritative operation recovery and proof-based retirement; job-safe drain; authenticated admin API/UI. Garm v0.2.1's small maintained pre-JIT compatibility patch is still required for benign defer semantics. No production-ready claim or live deployment is made.
