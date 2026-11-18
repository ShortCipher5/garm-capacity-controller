# Stock GARM JIT registration identity contract

This implements a credential-safe, read-only **pre-boot registration adapter**,
not a host authority issuer or physical lifecycle service. No command enables it
in production yet. It has no dependency on the experimental patched-GARM
preparation/dispatch protocol and changes none of that code.

## Supported upstream contract

Source pinned to GARM `154638445c3949c1958b01812f69d9a1e4d82684`:

- `runner/providers/v0.1.1/external.go:CreateInstance` sends the standard
  `BootstrapInstance` JSON on stdin and controller/pool identity in environment.
- `vendor/github.com/cloudbase/garm-provider-common/params/params.go` defines
  `jit_config_enabled`, `metadata-url`, `instance-token`, `name`, `pool_id` and
  `repo_url`. The payload does not directly contain the GitHub runner ID.
- `runner/pool/pool.go:AddRunner` obtains GitHub JIT configuration and its runner
  ID **before** storing the pending instance/provider creation work.
- `apiserver/routers/routers.go` exposes GET
  `/api/v1/metadata/credentials/{fileName}`. `JITCredentialsFileHandler` prefixes
  a dot, and `runner/metadata.go:GetJITConfigFile` decodes the stored JIT file.
  `validateInstanceState` permits pending/installing runner states, not active
  ones. This is consequently a pre-boot capture, not a later lookup guarantee.
- `util/github/client.go:GetEntityJITConfig` takes that configuration from the
  GitHub JIT API. The standard Linux bootstrap already fetches
  `credentials/runner`, so no GARM change is required.

Source links:
https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/providers/v0.1.1/external.go
https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/pool/pool.go
https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/metadata.go
https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/internal/templates/userdata/github_linux_userdata.tmpl

The .runner shape comes from GitHub runner `RunnerSettings`, not a promised
version-independent schema: `agentId`, `agentName`, `gitHubUrl`, `ephemeral`.
https://github.com/actions/runner/blob/main/src/Runner.Common/ConfigurationStore.cs
The adapter requires those fields with the expected types, positive int64 ID,
exact expected name/URL and ephemeral=true. Missing/changed formats fail closed.
Unknown extension fields are ignored and not copied into output. Duplicate field
aliases, malformed/trailing JSON, oversized responses and non-200 responses fail.
The .runner `poolId` is NOT a GARM pool ID and is deliberately not used as one.

## Credential and request boundary

Deployment policy supplies exact HTTPS metadata URL, GARM controller/pool and
GitHub scope URL. Bootstrap values must match before transmitting any token.
Only `/api/v1/metadata/credentials/runner` is requested, using the supplied
per-instance bearer token. The adapter never requests `.credentials`, RSA keys,
registration tokens, or arbitrary filenames; never executes bootstrap code.

Normal certificate/hostname verification is mandatory. The transport disables
ambient proxies, redirects, compression and automatic application retries, with
bounded body/header sizes and request timeouts. The configured DNS/CA environment
and service administrator remain trusted. A successful lookup proves only that
the trusted configured GARM metadata service returned this registration identity.
It is not independent GitHub API verification or a lease on that registration.
Errors are fixed strings; token, response body and ignored fields are not logged,
returned or persisted. The adapter does not make secret-memory-erasure guarantees.

Supported first mode: stock provider v0.1.1, JIT enabled, direct HTTPS standard
metadata path and .runner containing exact GitHub scope URL. Non-JIT and Gitea
registration, HTTP-only metadata, reverse-proxy path prefixes, differing URL
canonicalizations or missing .runner fields are explicitly unsupported, not
silently downgraded. No real deployment compatibility is claimed from fixtures.

## Concrete no-fork host lifecycle design

A host-local daemon must own the mutation credential/socket, with GARM's external
provider using only its authenticated RPC and no parallel direct Incus writer.
Incus documents that the privileged local socket grants full access:
https://linuxcontainers.org/incus/docs/main/authorization/
Removing alternative nontrusted mutators and restricting socket/certificate
access is a deployment prerequisite, not something an RPC boolean proves. Host
root/Incus administrators, the daemon OS account, GARM registration authority,
GitHub and the configured trust anchors remain trusted. Guest workflow users do
not. VM isolation is preferable where jobs require root or nested containers.

Before boot, the provider captures the registration above and must durably bind
it ONCE to controller/pool/request, host authority epoch and exact Incus
UUID/generation. Retry must recover the original record, never replace it with a
fresh name lookup or a later registration. That creation/identity integration is
not implemented here; the existing denied experimental dispatch binding is not
reused or enabled.

Natural completion can then match an authenticated GitHub job completion for the
exact registration (scope, runner_id, job id, run id and attempt), repair missed
webhooks using the job API, and combine it with host-controlled termination.
One-job ephemeral semantics prevent a second assignment, but a guest callback,
`busy=false`, deregistration or a stopped VM alone is not that combined evidence.
The host must serialize against start/rename/recreate/restore and reject stale
controller epochs before #99's qualified executor/finalizer can be enabled.

Unused warm runners still require a separate safe session-drain protocol. No
amount of polling idle status closes the assignment race. This adapter does not
solve that boundary, issue a retirement permit, delete resources or release any
charge. Its useful result is the previously missing supported **registration
identity observation before guest boot**, not fabricated host exclusivity.
