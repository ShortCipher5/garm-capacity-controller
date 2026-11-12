# Experimental credential-isolated gateway

This slice adds actual Go executables and a provider-only RPC boundary. **It is not production-qualified and has not been run against live Garm or Incus.** Destructive lifecycle, proof-based retirement, job-safe drain and broader Garm administration remain incomplete; separate TLS read/write roles and a real React ledger policy editor are implemented. Do not replace an existing provider or enable production scheduling from these examples.

## Processes and trust

- `capacity-gateway` is the only process with Incus credentials, the Garm inventory token and the SQLite ledger. Run it as a dedicated OS identity with a private configuration/key directory.
- `capacity-provider` is the external binary called by patched Garm v0.2.1 (`interface_version = "v0.1.1"`, `admission_enabled = true`). Its separate private configuration contains **only gateway endpoint/controller identity and gateway-client TLS files**. The Garm user must not be able to read gateway-owned Incus keys, inventory tokens, ledger or configuration. Do not run both under the same user and call that credential isolation.
- The gateway initially binds only an explicit loopback IP/port. Every HTTP request still requires a verified client certificate chain **and an exact allowed client-leaf SHA-256 fingerprint**. One controller/policy scope per listener. CA membership alone does not authorize other controllers. TLS 1.3 is required at this boundary; never terminate client authentication in an untrusted proxy.
- Configurations, CA files, certificate chains and private keys must all be owner-only regular files, owned by the process user. Final symlinks, shared permissions, oversized files, directories and FIFOs are rejected. Ancestor directories and the service owner remain trusted. Keys are not written to SQLite, command-line arguments or environment variables.

Build/check locally without contacting infrastructure:

```sh
go test -race -count=1 ./...
go vet ./...
go build ./cmd/capacity-provider ./cmd/capacity-gateway
go run ./cmd/capacity-gateway -help
```

## Protocol and behavior

The provider supports `AdmitInstance`, `CancelAdmission`, `CreateInstance`, `GetInstance`, `ListInstances` and non-network version/interface/schema discovery. It validates the exact Garm interface/controller/pool environment and bounded stdin. Admission preserves exit 75 plus the strict no-dispatch receipt. Unknown gateway/create outcomes return a constant error, **never not-found or a redispatch permit**.

The RPC routes are POST-only `/v1/admit`, `/v1/cancel-admission`, `/v1/create`, `/v1/get` and `/v1/list`. No admin, reconciliation, secret, policy, delete, stop/start or remove-all route exists. Request bodies are limited to 1 MiB with exact, duplicate-free struct fields, including nested bootstrap/tool options. Responses are bounded too. Redirects and ambient proxies are refused. There are no cookies, browser local storage, permissive CORS headers or browser-facing authentication shortcuts. In-flight handler work is capped; admission and intent remain serialized in SQLite.

Creates remain **off by default**. Starting the gateway without `-allow-experimental-provisioning` permits authenticated reads only and rejects reserve/cancel/create routes. The opt-in flag is an experimental operator action, not a certification: exclusive physical scopes, trusted profiles/images/credentials, blocked alternate provisioning paths and old-writer fencing must be established separately.

Before serving, and periodically afterwards, the gateway persists a fresh reconciliation barrier and reads Garm, Incus instances and operations. Failed/incomplete/active/unbounded inventories keep new dispatch blocked. Stored SQLite policy wins over `initial_policy` when opening an existing database. There is no API that silently replaces persisted budgets. A server restart does not retry an old dispatch intent.

The cancellation command proves only never-dispatched status within the authoritative ledger and writes a permanent no-recreate tombstone. Any dispatch intent, unknown/confirmed create or linked resource refuses cancellation. No physical delete is sent. See [cancellation constraints](undispatched-cancellation.md).

The periodic read is **not** the admission safety boundary. Every create still requires the durable one-shot claim and explicit limits. Project inventory alone cannot certify the entire physical host. An already committed claim is in-flight, not remotely fenced by a later reconciliation/drain.

Get/List expose bounded provider views without fault payloads or bootstrap. A failed Get is unavailable rather than not-found. **Delete, RemoveAllInstances, Start, Stop and pool-validation mutation commands intentionally fail closed.** This means normal Garm cleanup/retry lifecycle is not yet an operationally complete replacement for the stock provider. Do not work around it by routing deletes directly to unrestricted host credentials.

Allocation decisions/intents/outcomes remain in the durable audit ledger. RPC completion/rejection and TLS/HTTP connection errors emit constant redacted events to the service logger, never request bodies, panic values, URLs or raw upstream errors. This is a basic operational trail, not a complete admin security-event/retention system. Protect and retain service logs separately. Certificate/key rotation currently requires replacing private files and restarting; runtime revocation, administration and audit export remain future work.

## Private configuration shape

Provider configuration, owned by the Garm execution user (paths are illustrative):

```json
{
  "gateway": "https://127.0.0.1:9443",
  "controller": "controller-example",
  "tls": {
    "ca": "/private/provider/gateway-ca.pem",
    "certificate": "/private/provider/client.pem",
    "key": "/private/provider/client.key"
  }
}
```

The gateway server certificate must actually cover the configured endpoint IP/hostname. Never disable verification to make this example work. The allowed fingerprint is lowercase SHA-256 over the provider client's **DER leaf certificate**, not the CA, PEM text or key.

Gateway configuration has these top-level keys:

- `listen`: explicit loopback IP/port, e.g. `127.0.0.1:9443`.
- `controller`, `database`: fixed controller identity and private local SQLite path.
- `initial_policy`: the ledger's global/host/pool budgets, host modes/nonzero initial observation timestamps, and `max_observation_age_ns`. New ledgers always start blocked, regardless of initial health. Existing persisted policy is not overwritten.
- `pools`: map of pool IDs to `host`, `cpu_millis`, `ram_mib`, `flavor`, `image`, `image_fingerprint`, `profiles`, `type`, `arch`. Scalesets must use the same pseudo-pool identity as the compatibility hook.
- `hosts`: map of host aliases to trusted `endpoint`, `project`, and private `tls` file paths.
- `garm`: trusted `endpoint`, private `token_file`, `tls` files, exact `providers` names and explicit numeric `scalesets` to pseudo-pool mapping. Garm inventory is read-only HTTPS plus bearer auth; no token login/refresh or credential saving is performed.
- `server_tls`: gateway-server certificate/key and provider-client CA file paths in the same TLS-file shape.
- `provider_fingerprints`: exact allowed client certificate fingerprints. Do not allow a broad unrelated client population.

Use secret storage/OS credential delivery appropriate for the deployment. These examples contain no real addresses, keys, token, certificate or infrastructure configuration. `.local.json`, `.token`, PEM/key files and private workspace folders are ignored by Git, but review every commit independently.

## Restore and remaining acceptance gates

[The backup/restore library](backup-restore.md) uses SQLite's consistent online backup and produces admission-blocked standalone artifacts; it is **not a live recovery service**; the local `capacity-ledger` CLI wraps its offline primitives. Restore into a new path only after stopping/fencing all old external writers. A fresh complete authoritative Garm/Incus/operation inventory, including post-backup resources and operations, is mandatory before opt-in scheduling. The service cannot revoke a different process's credentials or make an old database harmless.

Remaining gates: installed-version/physical-scope qualification; executable compatibility against the exact Garm deployment; authoritative recovery and proof-based retirement; job-safe deregistration/drain; broader Garm administration; security-event retention and credential rotation procedures. No merge, deployment or production-ready claim is made by this slice.

The [pinned source-to-executable qualification suite](protocol-qualification.md) exercises actual upstream Garm calls through the compiled facade and this service, including ambiguous create correlation and no redispatch. It remains local fixture qualification, not permission to enable production scheduling.



## Immutable daemon-binding upgrade gate

The gateway now requires [durable schema-four daemon bindings](durable-host-bindings.md)
before physical dispatch. Actual TLS-key/origin/project/name/version observations
must match the complete persisted alias set; unknown or changed bindings stay
blocked. Historically charged unbound hosts cannot be automatically imported after
upgrade, even if later cancellation removes their last reservation. Qualified
scope import is not implemented. Keep the read-only admin view available for this
status; never clear state or use an older image to bypass it. This is not physical
hardware registration, foreign-project coverage, native parent-limit proof or an
external credential fence. Runtime missing-ledger/expected-lineage qualification
also remains open until its separate slice lands.

The optional admin listener supports a separately credentialed, CLI-opted-in ledger policy editor. See [management UI](admin-ui.md) for `management_token_file`, `--allow-experimental-management`, atomic conflict/audit semantics and the explicit no-drain/no-placement boundary.

