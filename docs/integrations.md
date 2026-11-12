# Integration evidence and gates

## Garm v0.2.1

The repository implements a patched Garm v0.2.1 pre-JIT hook, compiled `capacity-provider`, authenticated `capacity-gateway` and real read-only Garm/Incus clients. The [pinned source-to-executable suite](protocol-qualification.md) exercises that path against disposable mutual-TLS API fixtures. It has **not** qualified an installed Garm/provider stack, real GitHub job execution or native Incus enforcement.

Public source inspected: [Garm v0.2.1](https://github.com/cloudbase/garm/tree/v0.2.1), specifically the [external provider v0.1.1 create path](https://github.com/cloudbase/garm/blob/v0.2.1/runner/providers/v0.1.1/external.go). Garm invokes the external executable with `GARM_COMMAND=CreateInstance`; process execution errors are returned to its caller. The [pool create path](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/pool/pool.go#L1131) attempts provider deletion after a create error; its [pending-create worker](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/pool/pool.go#L1755) records `InstanceError` when provisioning fails. Therefore returning a generic provider error is **not** a proven benign defer/retry protocol, nor proof that refusing a create leaves a queued job retryable. The exact installed provider binary version and its behavior must be recorded separately.

The maintained compatibility patch adds a strict opt-in pre-JIT defer outcome and never-dispatched preparation-failure cancellation. The actual upstream normal-pool/scaleset tests and compiled-provider fixture gate cover these boundaries. Stock Create errors still mean failure, not deferral. See the [patch contract and limits](../integration/garm-v0.2.1/README.md).

Remaining installed-stack and lifecycle acceptance must establish:

- Exact Garm commit, provider binary version/hash, API version and configuration.
- Pool create/update fields, allowed minima/maxima, enabled semantics, labels and whether existing idle runners remain eligible for dispatch.
- Capacity denial behavior for normal/scale-set pools, runner DB state, cleanup, GitHub queue retention and later retry. Do not invent an HTTP 429 or provider exit-code contract.
- Concurrent cross-repository creates; repeated provider invocation; create timeout after an Incus operation succeeded; controller crash before/after reservation and before/after provider response.
- Durable request identity across retries and safe mapping of generated names, Garm IDs and Incus IDs.
- Adapter bypass resistance, including manually invoked provider commands and alternate provider/host routes.
- Reconciliation after missing/stale/partial/paginated inventory and delete-timeout recovery.

## Incus

Use the official [Incus REST API](https://linuxcontainers.org/incus/docs/main/rest-api/) and [security guidance](https://linuxcontainers.org/incus/docs/main/security/). The concrete transport uses verified HTTPS/mTLS, explicit project-scoped bounded instance/operation collections, a pinned local image and explicit CPU/RAM limits. It refuses clustered or unknown Incus topology. The gateway installs a durable reconciliation barrier before collecting Garm, Incus instances and operations. These API fixtures do not qualify the installed Incus version, native isolation, physical-host/project exclusivity, trusted profiles/images or competing writers. A project list is not proof of complete physical-host coverage. Credentials are supplied through private operator-owned files; no real credentials are shipped. See [the transport contract](incus-transport.md) and [gateway trust boundary](gateway-service.md).

Host aliases are accounting identities, not verified physical-server identities. Do not configure aliases or projects on the same physical host as independently budgeted hosts: the current configuration does not reject every such duplicate mapping, and separate per-host budgets cannot protect that shared machine. Prove unique physical placement and complete exclusive scope coverage before live admission; global accounting alone is not a substitute for each physical host cap.

## Drain is not disable-create

Denying admission stops new provisioning only. Existing idle runners may receive work, and a job can be assigned while a runner is being deregistered. An eventual drain workflow must first block creates, then coordinate dispatch eligibility, track busy runners to completion, confirm unregister/delete outcomes, reconcile ambiguous responses, and only release allocation after confirmed absence. Force termination must be an explicit separate action, never the default drain behavior.

The stock public Incus provider source was inspected at [cloudbase/garm-provider-incus](https://github.com/cloudbase/garm-provider-incus/tree/d3cc787b39365df446f67f2b6e1e68a257ea29ab). That source is not evidence of the installed provider version. This repository ships its own gateway-only provider executable and the maintained Garm source patch; it does not establish compatibility with an installed stock Incus provider binary. Destructive provider commands deliberately fail closed, so this is not yet a complete operational replacement.
