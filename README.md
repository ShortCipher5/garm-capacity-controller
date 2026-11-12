# Garm Capacity Controller

A lightweight, safety-first control plane **above [Garm](https://github.com/cloudbase/garm)** for GitHub Actions runners on multiple [Incus](https://linuxcontainers.org/incus/) hosts.

> **Experimental foundation, not a production controller.** This staged implementation contains the offline model, SQLite ledger, concrete verified API clients and an experimental local-only gateway/provider pair. Provisioning is off by default and requires explicit operator opt-in after deployment gates are addressed. It has not been deployed to an existing installation. There is no production web UI or complete retirement/drain lifecycle yet.

## Why this exists

Independent repository pools can each have a valid `max_runners` while their combined CPU, RAM and runner demand exceeds a shared host. Lowering pool maxima periodically is not an atomic admission mechanism. Two pools can provision concurrently between updates, and existing idle runners can accept jobs without new provisioning.

The target application manages:

- Multiple Incus hosts, enabled / draining / disabled states and health.
- Global and per-host CPU, RAM and runner budgets, with explicit system headroom.
- Repository-specific Garm pools, labels, priority and desired idle capacity.
- An atomic cross-pool provisioning boundary, durable reservations and reconciliation.
- Historical allocated capacity, observed usage, decisions and audit events.
- An authenticated, simple React UI backed by Go. SQLite is the transactional-store target for inventory, policy and history; its durable core is implemented in the `feat/sqlite-ledger` slice, with further qualification gates documented.

## First slice: offline admission simulator

The implementation is under review in [draft PR #1](https://github.com/ShortCipher5/garm-capacity-controller/pull/1). Before it is merged, check out `feat/durable-admission-foundation` to run the commands below. The default branch currently holds the project documentation.

The original JSON-backed Go foundation serializes admission across pools and processes sharing one local ledger. It reserves capacity **before** returning success, persists through restart, and charges every reservation state, including creating, idle, running, failed and unknown. It fails closed for disabled/draining/unhealthy/stale hosts, missing pools, invalid resources and exhausted budgets. Failed or unknown creates do not release capacity. No release mechanism is implemented in this slice.

A JSON ledger is used deliberately for this small testable slice, not as the final history database. It uses a separate advisory file lock, atomic replacement, file and directory sync. It is Linux-only, single-machine, local-filesystem storage. It is **not** distributed consensus, a multi-host database or proof of live provisioning safety. All callers must cooperate with the same lock. Uncertain persistence blocks that Store instance until it is reopened.

### Try it locally

This integrated facade branch requires the Go compiler 1.25+ (matching its pinned provider-common dependency) on Linux. The SQLite branch also requires CGO and a C compiler for its pinned SQLite driver. No credentials or infrastructure are needed.

```sh
go test -race ./...
go vet ./...
go run ./cmd/capacity-sim -ledger /tmp/demo.ledger.json -key request-1 -pool example/app -host host-a
# Repeat the same request: idempotent replay, no second reservation.
go run ./cmd/capacity-sim -ledger /tmp/demo.ledger.json -key request-1 -pool example/app -host host-a
```

The sample has two synthetic hosts and finite CPU/RAM/runner budgets. It reports a simulation decision as JSON. Host observations are synthetic and expire after five minutes. Delete **only your disposable demo ledger** to start a new scenario. The command never contacts an external API.

## Safety boundary

A budget is a bound on declared allocations, not actual CPU utilization or memory safety. CPU is in integer millicores; RAM is in MiB. Explicit headroom must be subtracted from available capacity. Shared vCPUs are not dedicated-core guarantees. Incus resource limits and job isolation remain separate requirements.

A real enforcement mode requires *every* provisioning route to use this admission boundary before the provider creates an instance. Periodic observation is insufficient. Reconciliation must merge Garm runners, Incus instances, in-flight operations and reservations without gaps or double counting. A lost provider response is an unknown resource, not a free slot. Unknown inventory blocks admission until bounded accounting is established.

Stopping new creates does **not** stop existing idle runners receiving jobs. Draining must account for runner deregistration / dispatch eligibility, in-flight job assignment and confirmed resource deletion. Never interrupt a running job implicitly.

The `feat/sqlite-ledger` branch adds transactional SQLite admission, audit and non-releasing reconciliation barriers; see [SQLite core design](docs/sqlite-ledger.md) on that branch. It is not a live adapter or production enforcement mode.

See [architecture and invariants](docs/architecture.md), [integration gates](docs/integrations.md), [security](SECURITY.md), and the [roadmap](docs/roadmap.md).

A pinned pre-JIT Garm compatibility hook and executable protocol harness are being developed on `feat/garm-admission-compat`; see [the integration tradeoffs](integration/garm-v0.2.1/README.md) on that branch. A credential-isolated real gateway is still required.

The `feat/gateway-dispatch` slice adds a durable one-shot backend execution boundary behind those receipts; see [gateway dispatch](docs/gateway-dispatch.md) on that branch. Concrete Incus transport, credential isolation and confirmed retirement still require qualification.

## Status and verification

Implemented across the stacked draft branches: conservative admission, SQLite reservations/audit, one-shot dispatch, verified Incus transport, WAL-consistent backup/restore, Garm inventory and barrier-first reconciliation, and experimental credential-isolated provider RPC. Not qualified: production deployment, complete physical-host coverage, admin authentication, retirement/release, historical telemetry, priority scheduling or job-safe drain. CI includes the model and actual pinned Garm compatibility harness. Passing CI is not installed-infrastructure or hardware-safety certification.

No live infrastructure was accessed or changed during initial development. No production-ready claim is made.

The `feat/incus-transport` slice adds a concrete, credential-verified Incus REST transport with explicit allocation limits and bounded project inventory; see [transport boundary and remaining gates](docs/incus-transport.md). Tests use disposable mutual-TLS fixtures, not live hosts. No gateway service or production mode is enabled.

The `feat/provider-facade` slice binds trusted fixed pool placement, official Garm bootstrap rendering, one-shot dispatch/readback and barrier-first Garm/Incus/operation reconciliation. It is not exposed as a gateway service yet. See [provider facade](docs/provider-facade.md).

The `feat/gateway-rpc` slice adds separate `capacity-gateway` and `capacity-provider` executables with verified mTLS, exact client-leaf allowlisting and read-only defaults. See [service trust, configuration and unfinished lifecycle gates](docs/gateway-service.md).
