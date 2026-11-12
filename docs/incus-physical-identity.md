# Read-only Incus physical identity observation

`incusapi.Client.PhysicalIdentity(ctx)` returns an opaque comparable
`PhysicalServerIdentity` after a real authenticated mutual-TLS root request. This
is a narrow building block for rejecting duplicate host aliases, NOT physical-host
attestation, durable binding, writer fencing or allocation/recovery authority.
No provisioning, stop, delete, registration or database change is performed.

The method requires:

- trusted API v1.0 and explicit `environment.server_clustered: false`;
- a nonempty bounded exact `environment.server_name`;
- one bounded public X509 certificate in `environment.certificate` with matching
  `certificate_fingerprint`, whose SPKI matches the ACTUAL verified TLS peer key;
- a canonical supported HTTPS authority. Unknown/malformed identity fails closed.

These fields are present in official [Incus v6.0.4 ServerEnvironment](https://github.com/lxc/incus/blob/a99413afd0c742da3bba10eb4f75d639f9dbbd32/shared/api/server.go).
The [root implementation](https://github.com/lxc/incus/blob/a99413afd0c742da3bba10eb4f75d639f9dbbd32/cmd/incusd/api_1.0.go#L278-L300)
reports the standalone OS hostname and network public certificate/fingerprint.
A TLS-terminating proxy presenting an unrelated front key is NOT supported.
Certificate-chain bundles, Unicode/zone-qualified authorities and clustered
servers remain unsupported rather than being inferred safe.

Available read-only getters are `Valid()`, `ServerKeyFingerprint()`,
`OriginFingerprint()`, `ScopeFingerprint()`, `ServerName()` and optional `ServerVersion()`.
Fingerprints are SHA256 strings; no raw address, certificate or credential is
exposed. External operator JSON cannot manufacture a nonzero authenticated value.

The origin fingerprint excludes project; a separate scope fingerprint binds the
canonical authority plus NUL plus the exact configured project, preventing project
changes from reusing an old scope binding. Neither a different scope nor whole-
struct inequality establishes a separate host budget. The origin normalizes hostname case, DNS trailing
dot, default HTTPS port and IP spelling. A second project on the same daemon is
not an independent physical budget. Distinct DNS aliases of one direct daemon
still share its key fingerprint. Caller policy must reject repeated key
fingerprints across host aliases regardless of project or reported name, as well
as repeated canonical origins. Shared keys on truly different hosts conservatively
collide too; qualify independent credentials rather than silently accepting them.

The getter always observes again and does NOT cache a lifetime identity. The
caller must persist and compare the qualified binding, reject key/name/origin
changes pending requalification, and never count a rotation or restart as new
physical capacity. A server or administrator with authority can lie or copy keys;
this is not protection against an external privileged adversary. Host exclusivity,
all provisioning paths and immutable backend-policy mapping are separate gates.

Tests use disposable credential-verified local servers, verify cross-project
identity equality and actual TLS key matching, reject missing/malformed/false
identity evidence, and demonstrate fresh observations and opaque JSON behavior.
No real server was contacted or modified.
