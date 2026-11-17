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
the flag is enabled, the three supporting tables
(`offline_pool_settings_revisions`,
`offline_pool_settings_request_receipts` and the installation marker
`offline_pool_settings_meta`) are created lazily, on first use
by the experimental code path — all three in a single transaction, never
at startup, and never by the stock
migration — and only while no experimental table exists at all (a genuine
first enable). If the installation state is inconsistent in any other
way — the marker present while a data table is missing, or the marker
missing while a data table survives — first use fails hard instead of
recreating anything (see "Installation marker, schema loss and reinstall"
below). Stock tables
(including `pools`) are never altered; the stock
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
| `Idempotency-Key` reused with a different payload or pool (see below) | 409 |
| generic failure | 503 |

`If-Match` accepts exactly one strong tag: `"<epoch>:<seq>"`. Tag lists,
weak tags and wildcards are rejected as malformed (400).

A successful PATCH (200) returns the full settings document and the new
`ETag`, exactly like GET. A no-op PATCH (all supplied values equal the
current values) succeeds (200) and does **not** advance the revision.

#### Optional idempotency

A PATCH may carry an `Idempotency-Key: <request-id>` header: an opaque
printable-ASCII string of at most 128 characters (longer or otherwise
malformed values are rejected with **400** and never persist a receipt).
The key makes the request safely retryable via a durable request receipt
(see below). GET does **not** participate in idempotency: the header is
ignored and no receipt is written.

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

`epoch` is minted monotonically, never a bare wall-clock value: a fresh
revision row gets `epoch = max(unix_now, marker.last_epoch + 1)`, where
`marker.last_epoch` is the highest epoch ever minted in this database,
recorded durably in the installation marker at mint time (in the same
transaction). Because the marker survives the revision table, an epoch
minted after a reinstall is strictly above every epoch ever issued in
this database — even when the reinstall completes within the same Unix
second as the original install — so no `"<epoch>:<seq>"` pair from a lost
or restored generation can ever match a post-reinstall revision row.
`seq` starts at 0 and increments on every committed, non no-op PATCH.
The ETag is `"<epoch>:<seq>"`, quoted.

## Atomicity

For PATCH, the revision check, the entity-membership check, the
idempotency replay check, the settings update, the revision advance and
the request receipt all happen in **one database transaction** (BEGIN
IMMEDIATE on SQLite). A stale precondition or a policy rejection rolls
back the settings write but still commits its receipt, so the rejection
itself is durable; a race detected at revision-advance time rolls back the
already executed settings update and is reported as a stale rejection.
Only generic failures persist nothing at all (see the receipts section).

Entity-scoped authorization is enforced in the same transaction: the pool
row is fetched keyed by both the pool ID and the path entity
(`id = ? AND <entity>_id = ?`), so a pool can never be updated through the
wrong entity path.

## Legacy writers participate in the revision scheme

Stock writers (e.g. `PUT /api/v1/pools/{poolID}` and the nested entity
pool PUTs) go through `UpdateEntityPool`, which now advances the
experimental revision of the pool **in the same transaction** as the
settings write: an inline, best-effort `UPDATE offline_pool_settings_revisions
SET seq = seq + 1 WHERE pool_id = ?`. A client holding an ETag therefore
observes legacy writes as conflicts (412) instead of silently overwriting
them.

Properties of the legacy bump:

- It **never creates experimental schema** and is silent only when the
  feature was never enabled: when the revision table does not exist
  (flag never enabled, or the pool simply has no revision row yet), the
  `UPDATE` matches zero rows or fails with "no such table", and either
  way the stock write is unaffected. A third table,
  `offline_pool_settings_meta`, is the durable install marker (see
  below): created in the same transaction as the revision table, its
  presence proves the revision table existed at install time. If the
  revision table is missing **while the marker is present**, the schema
  was lost after the feature was used, and the legacy write **fails and
  rolls back** ("experimental revision schema is missing after being
  installed; refusing the legacy write") rather than committing a change
  no conditional client can detect.
- It is **coarse**: any stock write that changes the `pools` row advances
  the revision, including writes that only touch columns outside the
  offline settings document (e.g. `image`). This errs on the side of
  over-invalidation: a client may be forced to re-GET after an irrelevant
  legacy write, but it can never miss a relevant one.
- It never double-bumps: the experimental PATCH path does not share code
  with the stock update path; it advances `seq` exactly once through its
  own conditional `UPDATE`.

## Installation marker, schema loss and reinstall

The lazy installer creates the revision table, the receipts table and a
marker table in **one transaction**, and seeds the marker row in that
same transaction. The marker row (`offline_pool_settings_meta`, single
row, key `schema`) holds:

- `installed` — always true once the row exists;
- `generation` — the durable installation generation: **1** on a fresh
  install, bumped by the explicit reinstall after table loss or a
  database restore. It lets operators (and future tooling) observe epoch
  resets; ETag validity is enforced by the epoch floor, not by this
  number;
- `last_epoch` — the highest epoch ever minted in this database, across
  all generations (see "Revision mechanism"). It is what makes reinstall
  epochs strictly monotonic.

Databases installed by code that predates the generation tracking carry
a **legacy marker row**: only `key`, `installed` and the timestamps (the
`generation`/`last_epoch` columns are missing from the table entirely).
On first use, in the same transaction as the presence check, the schema
code adds the missing columns and deterministically upgrades such a row:
`generation` is set to **1** and `last_epoch` to the highest `epoch`
among the surviving revision rows (0 when there are none). Every epoch
minted afterwards strictly exceeds every epoch a surviving row carries,
so the upgrade itself re-opens no collision window.

### What may create tables

Table creation may only run while **no experimental table exists at
all** — a genuine first enable. The full audit rule, enforced in one
place (`ensureOfflinePoolSettingsSchema`, called by both GET and PATCH
before they touch the schema; the legacy bump and the stock migration
create nothing experimental), is:

- marker absent, revision and receipts tables absent → install all
  three tables and the marker row (generation 1) in one transaction.
- marker absent, revision **or** receipts table present → **GET and
  PATCH fail hard** (`errOfflineSettingsSchemaMissing`, surfaced as 503
  on the wire). The witness is lost but data survives — the marker
  table was dropped after use, or a pre-feature backup was restored
  over a used schema. Silently installing would restart generation at 1
  and `last_epoch` at 0 over rows that may still serve ETags and
  receipts, re-opening every epoch/replay collision the marker exists
  to prevent. No table is created; recovery is the explicit reinstall.
- marker present, revision table missing → **GET and PATCH fail hard**.
  No table is recreated.
- marker present, receipts table missing → **GET and PATCH fail hard**,
  even though GET never writes receipts: a silent recreate would erase
  the replay record of requests whose responses may never have reached
  their clients.
- all three tables present → proceed; a legacy (pre-generation) marker
  row is upgraded in place as described above.

Silently recreating a lost revision table would restart revision history
at a fresh epoch while clients still hold ETags from the lost one,
silently recreating a lost receipts table would let an in-flight retry
double-apply a request the server can no longer prove it answered, and
silently reinstalling over surviving rows would reset the epoch floor
underneath them. All three are exactly what the hard failures prevent.

The legacy bump keeps using the same witness: revision table absent and
marker absent → the feature was never enabled against this database, a
benign skip. Revision table absent and marker present → the schema was
lost after the feature was used, and the legacy write fails closed (the
whole stock transaction rolls back). The legacy bump deliberately does
**not** consult the receipts table: losing receipts endangers replay,
not the revision invariant the bump protects; GET/PATCH are already
hard-failing in that state.

### The explicit reinstall

Recovery from table loss — or from a database restore, see below — is
the **explicit reinstall** (`ReinstallOfflinePoolSettingsSchema` at the
store layer; an operator-facing endpoint is deliberately not part of this
experimental slice). It is not reachable from any request path; GET and
PATCH never repair themselves. The reinstall, in one transaction:

1. recreates any missing table;
2. bumps the marker generation (generation+1);
3. **wipes every revision row and every request receipt**, so all
   outstanding ETags and all replay state are invalidated together;
4. preserves `last_epoch`, so revision rows minted after the reinstall
   get epochs strictly above every epoch ever issued in this database.

Consequence: every pool starts a fresh `seq` at a new epoch on its next
access, every pre-loss ETag is rejected stale (412), and every pre-loss
request id is treated as brand new. Clients holding an invalidated ETag
must re-read and re-send; clients retrying a request whose receipt was
wiped are answered by the normal conditional machinery (their old
precondition is stale), never by a resurrected outcome.

### Recovery policy: restores and rollback

Receipts and revisions live in the same database as everything else.
**A local epoch is not an anti-rollback authority, and no independent
anti-rollback authority exists.** Restoring an older database file over a
newer one rolls `generation`, `last_epoch`, `epoch`, `seq` and the
receipts table back to their snapshot values, and **the server does not
detect this in any way**: no fence, counter or external witness survives
outside the file that was just replaced. ETags that the live system had
already invalidated come back valid, receipts for requests that the
restore erased come back servable, and the marker happily reports the
rolled-back generation. Until recovery runs, a client could replay
against resurrected state.

The only safe recovery after such a restore is operator-driven, and it
is the explicit reinstall:

1. restore the database file (using SQLite's consistent
   backup/snapshot facilities or a clean closed/checkpointed database —
   copying the live main `.db` file alone is not safe);
2. run the explicit reinstall: the generation bump, the wiped revision
   rows and receipts, and the preserved `last_epoch` floor together
   guarantee that no ETag pair from the restored snapshot can match
   post-reinstall state. A restore that leaves the installation
   inconsistent (for example a pre-feature file restored over a used
   schema, so data tables survive without their marker) makes GET and
   PATCH fail hard with `errOfflineSettingsSchemaMissing` instead of
   silently reinstalling; the same explicit reinstall is the recovery
   there too;
3. a full restore plus reinstall **invalidates every outstanding ETag
   and every outstanding receipt**: all revision and receipt rows are
   wiped, so every client — whether it cached an ETag, a request outcome
   or an idempotency key — goes **UNKNOWN → fresh read**: it discards
   what it knows, GETs the current settings and revision, and resumes
   from there;
4. requests whose receipts were lost — in flight across the restore —
   **stay UNKNOWN until a readback plus an explicit operator decision**:
   the client (or tooling) reads back the current settings, reports the
   divergence to the operator, and only the operator decides whether the
   pre-restore intent still applies. Automatic re-submission is not safe:
   the restored settings may deliberately differ from the lost ones.

**Residual hole, documented rather than fixed:** after table loss (or a
restore), a later 412 does **not** prove that the lost request failed.
A 412 only proves that the presented precondition does not match the
current revision — the same signal an unrelated concurrent writer
produces. The lost request may have applied and been wiped; replay state
is exactly what the loss destroyed. This is why lost in-flight requests
stay UNKNOWN until readback plus an explicit operator decision, and why
the reinstall refuses to resurrect receipts.

One boundary case is now hardened rather than open: if the marker table
is lost **while the data tables survive** (marker dropped after use, or
a restored file predating the marker placed over a used schema), the
next use no longer reads "never enabled" — it **fails hard** with
`errOfflineSettingsSchemaMissing` and points at the explicit reinstall,
because silently installing at generation 1 with `last_epoch` reset over
surviving rows would re-open the epoch and replay collisions described
above. The witness-lost reading stays benign **only when nothing
survives**: all three tables absent is indistinguishable from a genuine
pre-feature database and installs fresh at generation 1. Note the
asymmetry the reinstall preserves in the surviving-tables case: it
recreates the marker (the lost generation count is unrecoverable, so
generation restarts at 1) and it does not wipe rows its fresh-install
branch did not lose — the surviving revision rows stay authoritative for
their pools. What the reinstall does **not** restore is the epoch floor:
with the old marker gone, `last_epoch` restarts at 0 and is rebuilt by
future mints, so the surviving rows' epochs are fenced only by the wall
clock. A full restore+reinstall avoids this by wiping every row.

## Durable request receipts

Every experimental PATCH persists a **request receipt** in its own
lazily created side table:

```sql
CREATE TABLE offline_pool_settings_request_receipts (
    request_id      TEXT PRIMARY KEY,  -- the Idempotency-Key (or a generated UUID)
    pool_id         uuid NOT NULL,     -- the pool the request targeted
    payload_hash    TEXT NOT NULL,     -- hex SHA-256 of the exact payload bytes
    presented_etag  TEXT,              -- the quoted If-Match value, NULL if malformed
    outcome         TEXT NOT NULL CHECK (outcome IN
                      ('applied_changed', 'applied_noop', 'rejected_stale',
                       'rejected_invalid', 'rejected_policy', 'rejected_malformed')),
    resulting_etag  TEXT,              -- observed (412) or produced (200) ETag, else NULL
    created_at      datetime NOT NULL
);
```

`rejected_invalid` is reserved and not produced by any code path in this
version. There is **no garbage collection or expiration**: rows accumulate
for as long as the table exists.

The receipt row is written in the **same transaction** as the settings
effect; rejected outcomes (which never touch the settings) commit their
receipt alone:

| HTTP outcome | persisted outcome | `resulting_etag` |
|---|---|---|
| 200 changed | `applied_changed` | new ETag |
| 200 no-op | `applied_noop` | current (unchanged) ETag |
| 412 stale | `rejected_stale` | observed current ETag |
| 400 body/precondition | `rejected_malformed` | NULL |
| 422 coupled limits | `rejected_policy` | NULL |
| 403 / 404 / 428 | no receipt at all | — |
| 503 | no receipt at all | — |

Requests rejected before the write transaction (403 non-admin or wrong
entity, 404 nonexistent pool, 428 missing `If-Match`) and generic
failures (503) never persist a receipt: the client cannot usefully retry
them blind. Note the 503 case is still **not** an outcome-unknown receipt
— no "unknown" outcome exists or is ever written; the atomicity guarantee
means no partial state can exist either.

A PATCH without an `Idempotency-Key` also persists a receipt, addressed by
a server-generated UUID: the receipt is durable but not replayable.

### Replay and id reuse

On a keyed request, the stored receipt for the key is consulted **before**
anything is applied:

- **Same `request_id`, same `pool_id`, same `payload_hash`**: the stored
  outcome is returned as the response, without re-applying anything and
  without writing anything. Applied outcomes are answered with **200** and
  the stored `resulting_etag`; rejected outcomes replay their original
  status (412 carries the stored observed ETag in the header and body).
  Because the replay document cannot reconstruct the original settings
  body (which is not stored, and the resource may have drifted since), a
  replayed applied outcome returns a small JSON document
  `{"outcome": "...", "resulting_etag": "\"<epoch>:<seq>\""}` instead of
  the settings document.
- **Same `request_id` but a different `payload_hash` or pool**: **409
  Conflict**. The error body explains the id reuse; no state changes and
  the original receipt is left untouched.

Identity is byte-based: two semantically identical payloads that differ
in any byte (e.g. whitespace) hash differently and therefore count as id
reuse.

## Nothing here is an outcome-unknown receipt

Every non-2xx response is a definite outcome: the request either did not
match the current revision (412), violated a documented constraint
(400/403/404/409/422/428), or the server failed definitively (503). The
503 case means the transaction failed; per the atomicity guarantee above,
no partial state — and no receipt — can exist.

## What is still not proven (explicit limitations)

- **Restore/rollback exposure.** Receipts and revisions live in the same
  database as everything else, with no automatic fencing: a full-file
  restore rolls epochs, sequences and receipts back, and nothing detects
  that the wall clock moved backwards. Recovery is the explicit,
  operator-driven reinstall documented under "Recovery policy: restores
  and rollback" above; until it runs, a client could replay against
  resurrected state. A local epoch is not an anti-rollback authority.
- **No GC.** Receipts accumulate forever; there is no expiration,
  retention or pruning. Operational handling of the growth is an open
  question.
- **Receipt identity is byte-based.** `payload_hash` covers the raw body
  bytes only (not the `If-Match`), so a retry must be byte-identical to
  replay; a changed precondition with the same key and payload replays
  the stored outcome instead of re-evaluating it.
- **Replayed applied outcomes do not return the settings document.**
  Only the outcome metadata is stored; the current document is available
  via GET.
- **Two racing keyed submissions** may both observe the stored outcome of
  whichever committed first; this is the intended at-most-once-apply
  behavior and not a conflict.
- **Legacy participation is coarse** (see above): over-invalidation is
  possible, under-invalidation is not.
- **Multi-dialect receipts.** The receipts and revisions tables are
  created through the ORM migrator and exercised on SQLite (CI); MySQL
  behavior is unverified in this slice.
- **The stock bump fails closed.** An unexpected side-table error aborts
  the stock write and rolls it back, and so does a revision table that is
  missing while the install marker is present (see "Installation marker,
  schema loss and reinstall" above). The only case in which the stock
  write proceeds without a bump is the feature never having been enabled
  against this database.
