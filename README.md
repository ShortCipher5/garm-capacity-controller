# Garm Capacity Controller

A lightweight Go control plane **above [Garm](https://github.com/cloudbase/garm)** for GitHub Actions runners on multiple [Incus](https://linuxcontainers.org/incus/) hosts, with a React management view and durable SQLite capacity ledger.

> **Experimental, not production-qualified.** The real gateway and provider integration are implemented and tested against disposable authenticated API fixtures. Provisioning is off by default. The web application reads the real ledger and optionally edits bounded ledger policy using a separate write credential. Physical retirement, automatic job-safe drain and installed-infrastructure recovery qualification remain incomplete. Do not replace an existing provider just because CI passes.

## Why this is needed

Repository pools can each have a sensible `max_runners` while their combined demand exceeds the CPU, RAM or runner budget of a shared host. Updating maxima periodically is not atomic enforcement: two pools can provision between updates, and existing idle runners can accept jobs without provisioning anything new.

Garm remains responsible for runner demand and GitHub integration. This controller adds:

- **One global admission transaction** covering global, per-host and pool CPU/RAM/runner budgets.
- **Durable reservations and one-shot dispatch intents** before physical creation. A lost response remains charged, not retried as a new create.
- **Barrier-first reconciliation** of Garm runners, Incus instances, operations and reservations, including orphans and unknown outcomes.
- **Explicit host enable/pause primitives**, without pretending that pausing claims prevents idle runners accepting jobs.
- **Durable daemon bindings** and fresh TLS pinning to reject known duplicate aliases and backend drift before dispatch. This is not whole-physical-host proof.
- **Credential isolation**: Garm receives gateway-only mTLS credentials, never Incus keys or database access.
- **A real React UI** for allocations, hosts, pools, recorded health, conservative drain blockers and audit events. Separately authorized, reviewed host-mode and budget changes; no mock fallback.
- **Consistent SQLite backup/restore primitives**, with admission blocked and new authoritative reconciliation required after restore.

The full goal also includes repository labels/priority/idle controls, safe lifecycle management and historical measured usage. These are not all implemented. Allocations are not measured utilization, and a priority field is not a fair scheduler.

## Current boundaries

| Component | Implemented | Remaining qualification |
| --- | --- | --- |
| Admission | SQLite WAL/FULL, `BEGIN IMMEDIATE`, atomic budget/reservation/audit and process-race tests | One authoritative local ledger, not distributed consensus |
| Garm integration | Pinned v0.2.1 pre-JIT compatibility patch and actual compiled-provider protocol harness | Requires the documented patch; installed stack not qualified |
| Provisioning | Verified Incus HTTPS/mTLS, explicit limits, durable at-most-once intent and readback | Experimental opt-in; full physical-host coverage must be proved |
| Reconciliation | Fresh-generation fence and bounded three-source inventory | Incomplete or unbounded inventory keeps admission blocked |
| Administration | Separate TLS read/write roles and responsive React ledger view | Opt-in separate write role for ledger host modes and budgets; no Garm pool editor or retirement button |
| Backend identity | [Immutable daemon bindings](docs/durable-host-bindings.md), transactional dispatch provenance and alias/TLS rotation tests | Legacy charged hosts need unimplemented scope import; hidden projects, multiple daemons per host and admission-ledger runtime lineage remain gated |
| Recovery | Consistent online SQLite backup/restore APIs | External writer fencing and live recovery drills required |
| Cancellation | Atomic never-dispatched tombstones, authenticated provider command and optional Garm preparation-failure hook | No dispatched/unknown-create or physical retirement release |

See [architecture](docs/architecture.md), [service trust and configuration](docs/gateway-service.md), [Garm integration tradeoffs](integration/garm-v0.2.1/README.md), [security](SECURITY.md) and [open acceptance gates](docs/operational-acceptance.md).

## Test without infrastructure

Requires Linux, the Go compiler **1.25+**, CGO and a C compiler for the pinned SQLite driver. Standard tests need no credentials or live infrastructure.

```sh
go test -race -count=1 ./...
go vet ./...
go build ./cmd/capacity-gateway ./cmd/capacity-provider
```

The required [pinned Garm qualification](docs/protocol-qualification.md) downloads exact public source into a **new disposable directory**, applies our patch, tests upstream demand paths and exercises the compiled provider against the actual gateway and disposable mTLS API fixtures:

```sh
integration/garm-v0.2.1/test.sh /tmp/garm-capacity-qualification-new
```

This additional gate needs Go 1.26.2+ to match upstream Garm. The standard Go suite deliberately skips it unless its explicit worktree is supplied. A unit-suite pass is not a pinned-integration pass.

The actual UI is in `web/`. Use supported Node.js 22.12+ or newer:

```sh
cd web
npm ci
npm test
npm run build
npm run test:ui
```

Browser tests use a disposable real TLS Go admin handler and SQLite ledger, not a production mock mode. See [UI setup and test-only certificate trust](docs/admin-ui.md). Serve built assets through the separate configured admin listener. Its read credential stays in memory and clears on disconnect; never put secrets in build variables.

The original JSON simulation remains for isolated experimentation:

```sh
go run ./cmd/capacity-sim -ledger /tmp/demo.ledger.json -key request-1 -pool example/app -host host-a
```

It never contacts Garm or Incus and is not the live enforcement store.

## Safety is more than a pool maximum

CPU is integer millicores and RAM is MiB. Budgets bound **declared allocations**, not actual utilization or shared-vCPU performance. Subtract host/system headroom; enforce Incus limits and job isolation separately.

Every physical provisioning path must pass the same authoritative ledger. Alternate credentials, other projects, queued work from an old writer and untracked host workloads can invalidate that boundary. A later mode change is not a remote fence for an already committed in-flight claim.

Creating, idle, running, failed, unknown and deleting resources stay charged. A timeout, missing list row, restart or provider failure does not prove safe release. [Never-dispatched cancellation](docs/undispatched-cancellation.md) requires an exact fresh creating reservation, no dispatch intent or linked resource, and a permanent no-recreate identity tombstone. Every dispatched or uncertain create remains charged.

**Paused is not drained.** Existing idle runners can still receive jobs. Physical retirement needs an assignment/job-safety fence, verified deletion and authoritative recovery. These paths remain blocked rather than hidden behind unsafe controls. See [job-safe retirement](docs/job-safe-retirement.md).

Runtime startup never creates or adopts missing state. Use [NEW-only initialization with independently protected lineage](docs/runtime-ledger-lineage.md). Supported archives/restores cannot be activated by this service.

Do not copy only an open WAL database file. Use the [consistent backup procedure](docs/backup-restore.md), fence old writers before historical restore, recover post-backup history and collect new complete authoritative inventory before scheduling.

## Next

Never-dispatched preparation cleanup, evidence-gated recovery and retirement, secure operator packaging, isolated self-hosted CI qualification, then broader authenticated Garm administration and historical telemetry. See [the roadmap](docs/roadmap.md). Passing local or browser CI does not establish native Incus isolation, a complete lifecycle or production readiness.

No live controller deployment is claimed. Tests must never access live runner infrastructure or commit credentials, private host addresses or user-specific configuration.

Local operator backup/restore: build `./cmd/capacity-ledger` and follow the
[fail-closed recovery procedure](docs/backup-restore.md). It produces new private,
WAL-consistent, admission-blocked artifacts only. External old-writer fencing and
a NEW complete authoritative reconciliation are still mandatory before use.
