# Native enforcement observation

`internal/nativeenforcement` reads the separate, authenticated
`GET /v1/native/enforcement?nonce=...` contract. It does not accept the existing
native identity endpoint as enforcement evidence. The observation is opaque,
expires after five seconds, and cannot be reconstructed from stored JSON.

The concrete client uses normal certificate-chain and hostname validation plus
an exact enrolled owner-server leaf pin. Its separate observer client identity
must not reuse the owner's execution-claim identity. Redirects, replacement TLS
verification callbacks, arbitrary transports and missing commitment fields are
refused. Every response binds a fresh nonce, the full expected owner/native
identity, capsule birth, retained root image, device filter, egress policy and
finite resource-limit digest.

This is not a pool-settings write permit. Issuing such a permit additionally
requires current canonical enrollment/profile/accounting linkage, an enforced
stock-provider path, management exclusivity, and the real native boundary. The
producer must refuse when that native boundary is unavailable. Current capsule
qualification remains guarded; no deployment or runtime activation is performed
by this observer. Tests use actual local TLS/mTLS transports, not an actual
qualified capsule or live GARM deployment.
