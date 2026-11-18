# Explicit retirement service gateway wiring

`native_retirement` is a separate private configuration containing `owners` (the
same exact enrolled policy/domain sequence, pinned owner endpoint and TLS fields
as native coordinator configuration) and `interval_seconds` (1–3600). It requires
`--allow-experimental-native-retirement` and an admin listener; the read-only job
observer option never enables retirement implicitly.

The production gateway refuses a configured retirement service before listeners
or private key reads. The builder and concrete service independently refuse
runtime-bound ledgers. This wiring does not qualify or override these guards.

In the offline/concrete service path, the gateway constructs immutable owner
coordinators, runs serialized bounded cycles under its lifecycle context, and
cancels and joins retirement before closing the canonical store. Outbound retirement
client leaves cannot also be owner claim or claim-server role leaves. There are
no detached proofs, user-selected native names or second allocation journal.

Authenticated `GET /admin/api/v1/native-retirement-status` reports the actual last
cycle and unresolved counts; it exposes no mutation endpoint. Completed operations
mean canonical STOP/DELETE receipt processing, **not** physical release or free
capacity. Charges remain retained. Before positive terminal/root evidence, the
service issues no retirement POST; uncertain/lost-response paths use existing
one-use journal recovery, never redispatch. Runtime/backend qualification and any
physical capacity release are separate prerequisites, not claims of this feature.
