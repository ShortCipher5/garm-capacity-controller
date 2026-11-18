# Dedicated canonical native claim listener (runtime refused)

The optional `native_claims` configuration contains `listen`, `tls` (private CA,
certificate and key paths), and `domain_binding_sequences` (1–256 distinct
positive canonical enrollment sequence IDs). It is separate from provider and
admin listeners and permits only literal loopback addresses at a distinct listener address.
Use a private transport without TLS termination if remote access is later
qualified; no proxy header conveys identity.

Production startup refuses configured claims before opening any listener or
reading keys. The builder and direct server constructor independently refuse a
runtime-bound store. There is no enabling flag or fallback. Configuration syntax
checking does not constitute activation or prove host confinement.

The offline builder reads the actual immutable enrolled owner leaves from the
same ledger. It rejects unknown/cross-controller domains, provider/source/stock
client overlaps, and provider/admin/owner server certificate overlaps. Enrollment
requires globally unique owner leaves, so a configured owner leaf cannot cover
an unlisted second domain. Request authentication requires verified TLS1.3 mTLS
plus the exact configured enrolled owner leaf; canonical transactions recheck the
owner against each immutable execution. No request can enroll an owner.

The listener delegates only to the existing canonical claim server: first-only
consume, stopped-create, bootstrap and start claims. No provider route, generic
native proxy, release endpoint, administrative action or manual receipt is added.
It inherits future reviewed fixed canonical claim routes rather than copying
their accounting. Unknown outcomes retain their existing charges.

Lifecycle wiring starts the dedicated service on the gateway's cancellation
context, cancels the gateway on unexpected listener exit, shuts down and joins
its serve loop, and reports an unexpected service failure rather than ignoring
it. Error logs remain redacted. Default configuration starts nothing.

Tests exercise actual verified mTLS owner exclusion, canonical rejection of an
unknown permit, no provider routes, immutable enrollment through a pinned TLS
observation, role collision rejection, shutdown join, and early runtime refusal.
No live owner process or physical boundary qualification is claimed.

Configured overhead-refresh recipes contribute their actual outbound TLS client
leaves to claim-role exclusions, including owner client, owner server and claim
server collision checks. Missing recipe credentials fail closed; runtime refusal
still occurs before reading any recipe or key. This does not activate claims.
