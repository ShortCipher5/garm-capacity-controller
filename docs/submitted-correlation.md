# Durable submission before operation wait

This is a narrow recovery hardening, not resource retirement, a release permit,
or installed-infrastructure qualification.

The credential-owning gateway uses the existing concrete Incus `Create` and
`Wait` methods as separate steps:

1. Commit the one-shot dispatch intent before the physical POST.
2. Send exactly one bounded authenticated create. Validate its operation UUID
   and expected resource name.
3. Commit `submitted` plus that validated UUID/name through the same opaque
   dispatch permit, before sending the first operation-wait request.
4. Wait, verify the existing allocation's exact identity/limits/running state,
   then record `confirmed`. Any error stays charged and cannot redispatch.

The submission write has an independent bounded five-second persistence context,
so request cancellation cannot erase a receipt already received. Failure stops
before Wait and returns uncertainty. If recording that uncertainty also fails,
the original durable intent still blocks another create and remains charged.
No raw bootstrap, provider fault or response metadata is written to the ledger.

A process killed during Wait leaves a durable submitted UUID/name for recovery.
A submitted receipt is **not terminal operation success**, job readiness or
safe deletion evidence. Even after confirmation, allocation charges remain.
Incus operation history may expire quickly; a future proof-based lifecycle needs
durable terminal evidence, backend/instance generation and external writer fencing.
This change does not claim to provide that complete evidence model.

The unavoidable gap between an external POST and a received/durable response
remains conservative. Lost POST responses, malformed identity and crashes before
submission persistence leave intent/unknown allocations charged. Empty operation
inventory, age, provider errors and missing list entries never authorize release.
Neither restart nor replay sends a second POST.

## Verification

Local tests cover a separate handle observing submitted state while Wait is
blocked, actual process kill during Wait, failed submission/audit persistence,
lost POST response and unchanged allocation charge/no recreate. The mandatory
actual pinned-provider fixture gate checks a separately opened ledger from the
Incus wait HTTP handler: submitted UUID/name must already be durable before that
request. Both confirmed and wait-failure modes still send exactly one physical
POST. These are disposable fixtures, not native Incus or live runner proof.
