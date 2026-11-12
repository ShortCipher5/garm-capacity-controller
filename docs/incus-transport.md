# Credential-verified Incus transport

Status: implemented REST transport, tested against disposable mutual-TLS HTTP fixtures, **not a deployed gateway or qualified physical-host enforcement**. The installed Incus version, project isolation, image and profile behavior still need qualification. No user infrastructure is contacted by tests.

`internal/incusapi` owns an HTTPS client with explicit server CA trust, expected hostname and client certificate/private key. It refuses insecure verification, ambient proxy configuration, redirects, URL credentials and arbitrary base paths. Each collection checks that the server reports a trusted API identity. Credentials stay in the process owning this client; a future Garm-facing facade must not receive the Incus private key.

## Concrete API boundary

- `GET /1.0` verifies trusted API v1.0. Creating additionally requires the `instance_create_start` extension.
- `POST /1.0/instances` is issued once, with a pinned **locally cached image fingerprint**, fixed profiles and explicit instance-level `limits.cpu` and `limits.memory`. Containers additionally set hard memory enforcement. Whole positive vCPU counts are supported initially. Mutable profile names are not themselves used as resource accounting evidence.
- Instance names, profiles, controller/pool labels, reservation hash and userdata are validated and bounded. No request-supplied arbitrary config/device maps, remote image servers, update, delete, quota mutation or general proxy are offered.
- An asynchronous create returns only a validated operation UUID and instance name. Operation waits verify the operation's project and resource identity, then require the instance state to be running. Failed/lost responses retain known correlation IDs. The transport never retries a create or deletes to compensate for uncertainty.
- Recursive instance and operation lists are project-scoped. Every instance, including unowned or orphaned instances, must expose finite expanded CPU and RAM limits. Missing, percentage-based or malformed limits reject the whole collection. Memory charges round upward to MiB; CPU-set ranges count their union. This is **declared allocation**, not measured utilization.
- Read results omit userdata, fault payloads, callback URLs and authentication material. Errors never include response bodies, bootstrap payloads or transport URLs. JSON duplicate critical keys and cross-project operation/resource paths are rejected.

## What this does not prove

This transport does not reserve capacity by itself. A trusted gateway must call it only from the one-shot `ExecuteDispatch` callback after durable admission and intent commit, and must reconcile Garm records, all physical allocation scopes and in-flight operations. A single project's list is not a complete physical-host inventory when other projects or administrators can create resources. Disallowed or unbounded allocation must block readiness, not be silently filtered out.

Explicit configuration does not prevent another administrator from changing limits later. Restrict host credentials, profile/device trust and other provisioning paths, qualify native host/project boundaries, and retain independent operating-system overhead headroom. Do not expose the transport directly to repositories or the browser.

Safe backup/restore, authenticated gateway RPC, policy-bound provider lifecycle and retirement/draining are remaining operational gates. No production enforcement mode or service endpoint is enabled by this slice.

## Pinned Garm read-path correction

The accompanying v0.2.1 patch corrects the upstream external provider's inverted successful-list error check. Its opt-in Get/List paths use the same bounded, redacted subprocess boundary as Create. Exit 30 is preserved as Get-not-found without retaining child stderr. Disposable upstream executable regression tests cover successful lists, not-found mapping and list failures. This is still a small maintained compatibility patch, not an upstream release.
