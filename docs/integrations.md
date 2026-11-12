# Integration evidence and gates

## Garm v0.2.1

The initial implementation has **not** run an installed Garm/provider stack. Compatibility is not certified.

Public source inspected: [Garm v0.2.1](https://github.com/cloudbase/garm/tree/v0.2.1), specifically the [external provider v0.1.1 create path](https://github.com/cloudbase/garm/blob/v0.2.1/runner/providers/v0.1.1/external.go). Garm invokes the external executable with `GARM_COMMAND=CreateInstance`; process execution errors are returned to its caller. The [pool create path](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/pool/pool.go#L1131) attempts provider deletion after a create error; its [pending-create worker](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/pool/pool.go#L1755) records `InstanceError` when provisioning fails. Therefore returning a generic provider error is **not** a proven benign defer/retry protocol, nor proof that refusing a create leaves a queued job retryable. The exact installed provider binary version and its behavior must be recorded separately.

Before an enforced mode is possible, disposable integration tests must establish:

- Exact Garm commit, provider binary version/hash, API version and configuration.
- Pool create/update fields, allowed minima/maxima, enabled semantics, labels and whether existing idle runners remain eligible for dispatch.
- Capacity denial behavior for normal/scale-set pools, runner DB state, cleanup, GitHub queue retention and later retry. Do not invent an HTTP 429 or provider exit-code contract.
- Concurrent cross-repository creates; repeated provider invocation; create timeout after an Incus operation succeeded; controller crash before/after reservation and before/after provider response.
- Durable request identity across retries and safe mapping of generated names, Garm IDs and Incus IDs.
- Adapter bypass resistance, including manually invoked provider commands and alternate provider/host routes.
- Reconciliation after missing/stale/partial/paginated inventory and delete-timeout recovery.

## Incus

Use the official [Incus REST API](https://linuxcontainers.org/incus/docs/main/rest-api/) and [security guidance](https://linuxcontainers.org/incus/docs/main/security/). A production adapter will need project-scoped instance inventory, operations and lifecycle events plus periodic authoritative reconciliation. Events can be dropped, so they cannot be the only accounting authority. TLS peer verification and narrowly scoped trust are mandatory. No client or credentials are present in this slice.

## Drain is not disable-create

Denying admission stops new provisioning only. Existing idle runners may receive work, and a job can be assigned while a runner is being deregistered. An eventual drain workflow must first block creates, then coordinate dispatch eligibility, track busy runners to completion, confirm unregister/delete outcomes, reconcile ambiguous responses, and only release allocation after confirmed absence. Force termination must be an explicit separate action, never the default drain behavior.

The current public Incus provider source was located at [cloudbase/garm-provider-incus](https://github.com/cloudbase/garm-provider-incus/tree/d3cc787b39365df446f67f2b6e1e68a257ea29ab). That source is not evidence of the installed provider version. No executable wrapper or provider modification is shipped here.
