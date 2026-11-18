# Native stopped-create, quota and start ledger (schema 21)

This is the canonical downstream ledger protocol, not production activation or
proof that a host has been exclusively fenced. It follows schema 20 and preserves
schema 19's immutable enrollment, initial one-use claim and reconciliation epoch.

The owner first receives its fresh parent claim through its own authenticated
exchange. It must then obtain a fresh `ClaimNativeCreate` response before sending
the exact stopped-create body. That step intent is durable and permanently bound
to the parent envelope, request hash and claim identity. No replay or lookup
returns fresh execution authority.

The controller reads the pinned owner acceptance endpoint and records the actual
accepted operation ID before accepting a terminal observation. The terminal read
is a separate endpoint; it cannot silently replace the accepted snapshot. A
successful CREATE must identify the same native incarnation, domain and policy.
Only an opaque live quota observation can record the stopped resource's actual
ZFS dataset GUID and exact `quota` bytes. `refquota`, unlimited, mismatched,
rounded, running or caller-generated quota evidence is refused. START requires
that exact CREATE success and fresh recorded quota evidence, then returns its
own single-use step claim for one fixed start request body.

Every accepted operation ID, terminal result, quota record and step identity is
immutable. A failed or unknown result prevents progression and is not converted
to success by a later observation. START success must retain the created native
incarnation. The overall canonical dispatch cannot record submitted/confirmed
for any bound native envelope without the corresponding START receipt, including
a consumed parent for which no suboperation intent has yet been written. Existing
historical submitted/confirmed rows survive migration, but legacy outcome APIs
cannot newly promote a native envelope without staged evidence. Neither CREATE,
quota verification, START nor any timeout frees capacity or authorizes deletion.

The observation package performs its own pinned mutual-TLS reads. Its exported
record structs are not proof constructors. The owner endpoints must derive their
responses from actual private native operations and root ZFS reads; they must not
accept arbitrary posted reports as evidence. Those endpoint implementations and
host exclusivity are separate integration work. Tests use explicit loopback test
backends and do not represent live host qualification.

Supported backup and restore preserve all suboperation history and close
readiness. The parent immutable epoch prevents progression across the required
new reconciliation. Raw active-image rollback, an unfenced stale controller,
owner restart/adoption and competing independent controllers are not solved by
SQLite-local claims. Production activation remains closed until the independent
host boundary is implemented and qualified.
