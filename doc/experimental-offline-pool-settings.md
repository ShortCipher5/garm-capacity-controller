# Experimental offline pool settings API

> **EXPERIMENTAL, OPT-IN, UNSTABLE.** Everything on this page is gated
> behind a config flag, disabled by default, and may change or be removed
> without notice. Do not build production automation against it.

The offline pool settings API is a small, offline-first vertical slice that
exposes a strict subset of the pool record (`priority`, `max_runners`,
`min_idle_runners`) with optimistic concurrency control (strong ETags).
It writes the same `pools` rows the stock API writes, but every mutation
is guarded by a revision check, applied in a single database transaction.

## Enabling

```toml
[experimental]
enable_offline_pool_settings = true
```

When the flag is disabled (the default) the routes are **not registered
at all**: requests to the paths below behave exactly like requests to any
other nonexistent route — GET gets the normal not-found handler (404),
PATCH gets the stock router's method-not-allowed response (405, since the
catch-all not-found route does not list PATCH) — and **no database schema
change of any kind occurs**. When
the flag is enabled, the single supporting table
(`offline_pool_settings_revisions`) is created lazily, on first use by the
experimental code path — never at startup, and never by the stock
migration. Stock tables (including `pools`) are never altered; the stock
schema and all existing routes behave exactly as before, flag on or off.

## Endpoints

```
GET   /api/v1/offline/entities/{organization|repository|enterprise}/{entityID}/pools/{poolID}/settings
PATCH /api/v1/offline/entities/{organization|repository|enterprise}/{entityID}/pools/{poolID}/settings
```

Both endpoints require an authenticated admin caller (JWT bearer token),
like every other API route. Deviating from the rest of the API on
purpose: non-admin authenticated callers get **403** from these handlers
(the rest of the API returns 401 for them via the admin middleware), and
unauthenticated callers still get 401 from the JWT middleware.

### GET

Returns the current settings as strict JSON:

```json
{"priority":0,"max_runners":5,"min_idle_runners":1}
```

plus a strong, quoted `ETag` header of the form `"<epoch>:<seq>"`.

### PATCH

The body is strict JSON over exactly the three fields above:

- at least one field is required;
- unknown fields, duplicate fields, non-integer values and noncanonical
  integer literals (leading zeros, signs, decimal points, exponents) are
  rejected with **400**;
- ranges: `priority` and `min_idle_runners` must be within `0..1000000`;
  `max_runners` must be within `1..1000000`. Out-of-range values are
  rejected with **400**;
- deviation from strict RFC 8785 canonicalization: field order is *not*
  enforced (any order of the three fields is accepted). Whitespace is
  otherwise insignificant as usual.

Preconditions and outcomes:

| condition | status |
|---|---|
| missing `If-Match` header | 428 |
| malformed `If-Match` (not exactly one quoted `"<epoch>:<seq>"`) | 400 |
| stale `If-Match` | 412, with the current `ETag` in the response headers and body |
| pool exists but is not a member of the path entity (including a nonexistent path entity) | 403 |
| pool does not exist | 404 |
| `min_idle_runners` > `max_runners` (checked against the current row inside the transaction, whenever either count is supplied) | 422 |
| generic failure | 503 |

`If-Match` accepts exactly one strong tag: `"<epoch>:<seq>"`. Tag lists,
weak tags and wildcards are rejected as malformed (400).

A successful PATCH (200) returns the full settings document and the new
`ETag`, exactly like GET. A no-op PATCH (all supplied values equal the
current values) succeeds (200) and does **not** advance the revision.

## Revision mechanism (design choice)

**Choice: one revision row per pool** in a dedicated side table
(`offline_pool_settings_revisions`: `pool_id` PK, `epoch`, `seq`), rather
than a global epoch/sequence singleton maintained by triggers.

Why:

- The ETag only needs to order mutations of *one* pool's settings. A
  per-pool counter is sufficient and avoids any global write contention
  between unrelated pools.
- The revision advance is a single conditional
  `UPDATE ... WHERE pool_id = ? AND epoch = ? AND seq = ?` inside the same
  transaction as the settings update, so a lost race is detected
  atomically and rolls everything back. No triggers are needed and no
  stock table is modified.
- Keeping the counter out of the `pools` table leaves the stock schema
  completely untouched, which is a hard requirement for this slice.

`epoch` is the Unix time at which the revision row was lazily created
(first GET or PATCH against that pool through this API) and never
changes; `seq` starts at 0 and increments on every committed, non no-op
PATCH. The ETag is `"<epoch>:<seq>"`, quoted.

## Atomicity

For PATCH, the revision check, the entity-membership check, the settings
update and the revision advance happen in **one database transaction**
(BEGIN IMMEDIATE on SQLite). A stale precondition or any failure leaves
both the settings and the revision byte-for-byte unchanged — including a
race detected at revision-advance time, which rolls back an already
executed settings update.

Entity-scoped authorization is enforced in the same transaction: the pool
row is fetched keyed by both the pool ID and the path entity
(`id = ? AND <entity>_id = ?`), so a pool can never be updated through the
wrong entity path.

## Legacy writers

Stock writers (e.g. `PUT /api/v1/pools/{poolID}`) update the same
`pools` columns but do **not** bump the revision, and this is a known,
explicit limitation of the provisional wire v0: after a legacy write, the
ETag does not reflect the new content, so a client holding an old ETag
can still "successfully" PATCH on top of changes it never saw. Clients
that intermix legacy and offline writers must re-GET before patching;
the GET response is always a self-consistent (content, ETag) pair. Making
legacy writers participate in the revision scheme would require touching
the stock update path, which this slice deliberately avoids.

## Nothing here is an outcome-unknown receipt

Every non-2xx response is a definite outcome: the request either did not
match the current revision (412), violated a documented constraint
(400/403/404/422/428), or the server failed definitively (503). The 503
case means the transaction failed; per the atomicity guarantee above, no
partial state can exist.
