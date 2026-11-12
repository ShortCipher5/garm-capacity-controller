# Pinned source to compiled provider qualification

This suite exercises the real public Garm v0.2.1 external-provider implementation, not a reimplementation of its protocol. It is **offline qualification against disposable API fixtures**, not a live deployment, installed-binary verification or Incus kernel/isolation certification.

## Executed path

```text
pinned Garm external provider (actual upstream Go code + documented patch)
  -> compiled capacity-provider process (real GARM_* environment and stdin/stdout)
    -> actual capacity-gateway service and private configuration
      -> SQLite reserve/dispatch journal
      -> concrete verified Incus REST client
        -> disposable mutual-TLS HTTP Incus fixtures
```

The gateway also obtains its baseline through the actual read-only Garm API client against a disposable mTLS inventory endpoint. TLS keys/certificates and all configuration are generated in private temporary files. User infrastructure, GitHub jobs, remote runner downloads and real Incus daemons are never contacted or executed.

The test verifies the exact pinned source HEAD before creating a fixed-name temporary qualification test with exclusive no-overwrite creation. It refuses to overwrite an existing source file and removes its own generated test on completion. The source patch's canonical artifact remains separate from these test-only additions. The direct opt-in test checks HEAD, not every worktree byte; the mandatory fresh `test.sh` path establishes source-and-patch provenance by fetching the pinned public commit and applying the repository patch into a new directory. Do not treat an independently modified worktree with the same HEAD as equivalent qualification.

## Never-dispatched preparation cleanup

Both modes first reserve an unused identity through actual Garm, cancel it twice
through the compiled provider, and reject re-admission/late Create. A new identity
then obtains the same global slot. The parent test checks the permanent tombstone,
absence of a dispatch entry for that cancelled key and still exactly one physical
POST overall. Confirmed and unknown dispatched requests both refuse cancellation.
The upstream pool/scaleset tests separately exercise JIT/row preparation failures;
this subprocess gate tests the actual protocol, gateway and database boundary.

## Confirmed outcome

Actual Garm calls pre-JIT admission, then Create twice, Get and List using its v0.1.1 parameter construction. The compiled facade uses the strict authenticated RPC and the actual service sends explicit CPU/RAM/local-image/correlation settings to the Incus fixture.

Assertions:

- Exactly **one physical create POST**, despite a repeated confirmed Create call.
- Garm accepts the actual provider output/identity/status.
- Real Get/List paths work, including the patched upstream successful-list condition.
- Filling the global budget causes the actual preflight subprocess exit 75 to be recognized by Garm as benign admission deferral, not Create failure.
- The durable dispatch record retains confirmed state, the operation UUID and resource name.

## Ambiguous outcome

A second isolated service/ledger fixture accepts one create, returns an operation UUID, then loses the operation-wait response with a server error. Actual Garm invokes Create twice.

Assertions:

- The error is **not** converted into pre-JIT defer or provider not-found.
- A replay never sends another physical create.
- The known operation/resource correlation survives in the durable unknown dispatch record.
- The allocation remains charged, so another preflight is deferred at the same global limit.
- Bootstrap tokens and raw private response faults do not appear in Garm's returned error.

This does not implement automatic recovery, release or cleanup of unknown work. Those remain explicit gates.

## Running and CI

The mandatory pinned-source workflow runs:

```sh
integration/garm-v0.2.1/test.sh /absolute/new/disposable-source-directory
```

It fetches exact public source commit `154638445c3949c1958b01812f69d9a1e4d82684`, applies the compatibility patch, runs the actual common/provider/pool/scaleset race tests, then explicitly runs `TestPinnedGarmCompiledProviderWire` against that source tree. The provider process itself is compiled with the race detector for qualification. Linux, CGO, a C compiler, Git, and a Go toolchain new enough for pinned Garm are required.

Fast `go test -race ./...` intentionally skips this opt-in source-dependent test when no disposable pinned worktree is supplied. **A fast-suite pass alone is not a pass of this end-to-end gate.** The pinned workflow invokes it explicitly, without silently treating a missing source as successful qualification.

The pool/scaleset source tests separately verify admission before disposable mock GitHub JIT allocation and runner-row creation. This wire test verifies the actual external-provider/executable/service boundary. Neither is proof of job-safe draining, a complete installed cleanup/retry lifecycle, all physical projects or native Incus limit enforcement. The gateway remains experimental and destructive commands remain deliberately unsupported.
