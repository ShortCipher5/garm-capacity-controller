# External writer fencing and physical-scope qualification

Status: **acceptance design and disposable counterexamples only**. No production
fencing authority, physical-asset registry, native-parent adapter, activation
path, release route or deployment is implemented by this prototype. No live
infrastructure was accessed. This document cannot authorize a live drill.

Baseline: schema 6 local `7cf1c1e`, identical tree to public runtime-lineage PR #29.
Placement and CREATE-history improvements do not close the external gates below.

## What is already proven, and what is not

Runtime startup rejects missing/wrong/offline/archive state using independent
lineage. Immutable daemon bindings reject duplicate configured keys/origins and
bind dispatch to observed daemon/project identity. SQLite serializes cooperating
writers of the SAME ledger. A local barrier stops NEW claims. These are useful
local proofs, not a physical-hardware or external-writer lease.

Three concrete counterexamples remain reproducible against the current core:

1. Two independently initialized runtime ledgers, each with a correct private
   identity and the same authenticated daemon binding, can each admit its full
   allowance. Two initially empty collections followed by sequential creates
   suffice: every ledger budget is respected while the shared fixture receives
   twice one physical allowance. No write-skew or concurrent SQL bug is required.
2. A callback paused AFTER its one-shot dispatch claim can still invoke its
   backend after another collector installs a local durable reconciliation
   barrier. The charge remains held; the barrier cannot revoke delivered work.
3. A complete successful list for the configured project omits a known foreign
   project allocation. One new managed create respects its ledger/project budget
   while doubling the physical fixture allowance. No transport error is hidden.

The opt-in tests assert these counterexamples still occur. Their green result is
**evidence of the gap**, never a passing deployment-safety gate:

```sh
go test -race -count=1 -v -tags external_writer_counterexample \
  -run '^TestCounterexample' ./internal/gateway
```

The shared backend is an in-memory fixture, while daemon identity uses disposable
loopback mTLS and each ledger uses real private runtime SQLite initialization.
There is no native Incus provisioning, credential change or hardware test.

## Required enforcement boundary

Choose one reviewed architecture, then test its actual implementation:

- A single external writer authority with an independent monotonic epoch, an
  independently registered physical scope, durable accepted-request history and
  a backend-side request-commit check; OR
- A sole credential-owning broker with equivalent durable sequencing, with native
  authorization/network policy making direct write bypass impossible, plus
  effective native parent resource limits covering every relevant writer.

A client-side lease check followed by an ordinary POST is NOT a fence. An old
request can pass its check, pause, then POST after the epoch advances. The final
write acceptance must be serialized with epoch validity at the enforced backend
boundary. There must be no provider, CLI, sidecar, alternate certificate, project,
loopback socket, proxy, SSH or root path able to bypass that boundary.

An external broker and Incus do not share a database transaction. A durable broker
intent plus a lost POST response is still ambiguous work. Record it before the
attempt, never redispatch it automatically, retain its charge, and refuse a
"drained" proof until authoritative resolution. Do not pretend a local mutex or
SQL transaction makes the remote side effect atomic.

Use two explicit milestones: **stop-new** is the monotonic authority epoch change;
**fence-complete** is the later verified revocation/handler/queue/native-operation
drain. Do not start the successor or issue a completed-fence receipt between
them. An epoch advance can reject new old-writer requests, but cannot undo remote
work accepted before it. Every
accepted handler, queued request, in-flight POST and native operation must remain
accounted for. Old credential revocation, process exit, a timeout, an empty list,
terminal-operation expiry or a fresh TLS handshake alone proves none of this.
Keep recovery/activation disabled while any required history is missing.

## Required acceptance matrix on an authorized disposable installation

Use the exact candidate image/version, native server version, credential policy,
transport protocols and service startup path. No model or pasted JSON receipt
can substitute for these measured assertions. Instrument the final native write
acceptance point and independently measure effective parent resource usage.

| Gate | Adversarial setup | Required measured result |
|---|---|---|
| F01 old credential | Two real gateways and separate local DBs; stop-new/rotate epoch/revoke A; keep B blocked until fence-complete | A cannot gain any new write acceptance through fresh or already pooled TLS connections after stop-new; preaccepted work stays tracked; zero old work can mutate after fence-complete, and B cannot resume before drain/history gates pass |
| F02 paused handler | Pause A after local admission/client lease check but BEFORE enforced authority acceptance; separately pause an already-authority-accepted request before native POST | First pause: stop-new makes resumed request fail, zero native operation/resource. Second pause: request was durably registered before stop-new, remains charged and blocks fence-complete until resolved; no false atomic broker/native transaction |
| F03 queued/streamed work | Queue provider retries and multiple keep-alive/HTTP streams before revocation; deliver them after it | Stale requests not yet authority-accepted are rejected at request-time, not merely connection establishment; already accepted queue entries block fence-complete until drained; no duplicate dispatch |
| F04 accepted old work | Accept a native POST before rotation; lose its response and let operation continue | Durable old intent remains charged; new epoch cannot be called drained/ready by old process death or absence from one list |
| F05 independent history | Restore an older raw ACTIVE local DB with SAME UUID and old writer credentials | Independent authority refuses stale epoch/history position; no automatic new epoch grant or adoption based solely on matching UUID |
| F06 authority failure | Partition/crash/restart the authority, lose its quorum, or roll back its storage | No cached-write fallback, operator boolean or local lease extension; missing monotonic history blocks all affected writes |
| F07 bypass enumeration | Exercise every actual direct native credential/socket/project/provider/sidecar/admin route | Every mutation enters the same enforced authority and native parent, or is denied; unknown route means NOT QUALIFIED |
| S01 physical mapping | Two independent daemon processes, keys, names, ports and projects on ONE physical machine | Registry maps both to one asset/parent allowance, or startup refuses the ambiguous topology; two daemon identities cannot double capacity |
| S02 hidden allocation | Preload a foreign/default/hidden project and another daemon with budget-consuming guests | Whole-asset authority includes them or an enforced common hard parent contains them; a project-complete empty list cannot establish whole-host completeness |
| S03 effective limits | Saturate workloads through both writers, including VMs, forks, swap, disk and restart/error paths | Actual CPU quota/cpuset, RAM+hypervisor overhead, swap, PIDs and required storage/IO limits match the pinned contract at the common parent; machine/daemon reserve remains available |
| S04 limit bypass/drift | Change project/profile membership, reparent a process/VM, restart daemon, rotate key or remap DNS/asset | No allocation escapes the qualified parent/asset; drift blocks affected provisioning pending requalification, not a new allowance |
| S05 inventory ordering | Barrier, then fresh Garm/native instances/active operations/full-scope queries while fence remains active | Unknown/partial/unauthorized source, active or ambiguous operation, missing scope or source identity blocks readiness; no release by omission |
| R01 recovery gate | Combine F01–F07 and S01–S05, post-snapshot history and contradictory observations | No current archive activation or release: a separately reviewed recovery design is still required; proofs do not create a generic "force" path |

For F01/F03 qualify the actual native authorization implementation. Do not assume
that deleting a certificate invalidates a previously authenticated HTTP request,
connection or operation. For F07 a native administrator/root escape is either
outside an explicitly trusted threat model or a failed exclusivity assumption;
record which. Claims about unprivileged users cannot silently cover privileged
foreign infrastructure.

For S03 distinguish effective HARD parent limits from project accounting limits,
profile defaults, daemon-reported bounds, periodic pool sizes and observed usage.
A shared runner parent must include all modeled guests across projects/daemons;
host OS and daemon headroom must be excluded from the admitted allowance. Native
caps can protect physical safety without satisfying the controller's global and
per-pool promises. Those promises still require a common atomic allocation
boundary, or a strictly single active writer with complete inherited history.

## Proof record and trust

A future qualification report should retain: exact build/source/image digests;
asset-registry generation and owner; all daemon SPKI/origin/project bindings;
external authority identity, epoch and monotonic history range; credential and
route coverage; fence linearization marker; every accepted pre-fence request and
its terminal/unknown accounting; fresh inventory barrier/order; effective parent
configuration plus independent measured resource trace; test IDs, timestamps and
counterexample outcomes. Preserve contradictions and UNKNOWN, not just passes.

The authoritative adapter must authenticate and bind those facts to the exact
asset/scope/install generation. An operator checkbox, unauthenticated report,
JSON blob, server name, host UUID, copied key, cgroup path string, receipt from an
old install or signed statement that omits a writer is not sufficient. This
prototype defines no verification API accepting such reports and cannot activate
or release anything. A future adapter needs separate security and failure review.

## Test-only executable specification

```sh
go test -race -count=1 -v ./qualification/authoritymodel
```

The `_test.go` model demonstrates necessary properties: final epoch check after
queueing, old accepted/unknown work blocks resume, raw same-ID snapshots cannot
roll back an external epoch, authority partitions and incomplete coverage deny,
and two daemon/project scopes share one atomic physical parent cap. A separate
check-then-write counterexample demonstrates why a preflight token is insufficient.

The model has no credentials, durable storage, consensus, native daemon, resource
controller, signed proofs or production code. Its mutex makes an abstract commit
atomic by assumption. It is deliberately NOT a deployable fencing adapter or a
claim that Incus provides conditional writer epochs. Passing it tests the
specification only. The native/distributed integration acceptance gates remain
OPEN until measured on the exact authorized disposable deployment.

## Current concrete blockers

- No backend-enforced monotonic writer/history authority or recovery activation
  adapter is implemented.
- Native credential revocation, pooled-handler drain and request-commit behavior
  have not been installation-qualified.
- No external unique physical-asset registry or effective common native-parent
  quota adapter proves complete project/daemon/writer coverage.
- Actual image/profile isolation, parent headroom and resource-controller behavior
  have not been stress-qualified.
- Garm job-assignment fencing and job-safe destructive retirement remain separate
  open gates. CREATE evidence alone is not DELETE absence or release authority.

Keep live scheduling, automatic host import, archive activation and destructive
lifecycle disabled until their specific gates are implemented and qualified.
