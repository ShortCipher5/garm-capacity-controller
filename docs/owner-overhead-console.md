# Explicit owner overhead collection from the existing server screen

Configure `admin.owner_overhead_refresh` with `recipes_file` and a pre-created
owner-private mode-0700 `journal_directory`. Existing experimental admin management
and its separate write credential must already be enabled. Neither this configuration
nor successful collection enables runner execution or GARM writes.

The private recipes file is an array of `{id,targets:[{binding_sequence,endpoint,kind,tls}]}`.
`kind` is `domain`, `artifact`, or `both`; `tls` contains private `ca`, `certificate`,
and `key` filenames. At most 100 recipes with 10 distinct existing enrolled bindings
per recipe are accepted. Endpoint, credentials and proof data cannot come from the
browser. Actual enrollment and host identity are checked at construction and each
collection. Recipe review revisions bind runtime ledger lineage, target identity,
endpoint, selected kind and actual configured trust/certificate material.

`GET /admin/api/v1/overhead-refresh` uses the admin read credential. It returns
public recipe IDs, revisions, enrolled hosts/binding sequences/kinds, and at most
100 recent operation receipts, plus actual process-running and can-start fields.
`POST` uses the separate management credential and same-origin/JSON checks. It
accepts only `{recipe,recipe_revision,request_id}`; request ID is 32 lowercase hex
characters. A 202 response is a durable pending receipt, not collection success.

One process/OS-locked journal serializes collection. Pending is persisted and
fsynced before owner reads. The background pass is bounded to 150 seconds; shutdown
cancels and waits for it. Each canonical recording is its own transaction; later
failure cannot roll back earlier debt. Final receipts preserve committed audit
sequences and distinguish complete, partial, and failed. Crash-interrupted pending
receipts appear unknown after restart. Missing terminal metadata cannot mean no
ledger effect. The same ID never automatically executes again. An explicit new ID
may request fresh observations after partial/failed/unknown outcomes; this never
releases existing debt. Journal I/O failure closes collection until repaired.

The journal is metadata, not another capacity ledger. Current canonical allocations
remain authoritative; old successful receipts are not current proof or readiness.
Retain its lineage marker and operation files with the service state. It refuses a
foreign runtime lineage and corrupt receipts. The service owner/ancestor directories
are trusted, as with its private credentials; this is not anti-rollback protection
against that owner. No automatic purge exists. At 10,000 retained receipts new
requests refuse before insertion; deployments must plan explicit metadata archival
rather than deleting unknown receipts or silently resetting history.

A separate deployment qualification is still required. No owner is contacted at
page load; only the authenticated explicit POST begins real collection.
