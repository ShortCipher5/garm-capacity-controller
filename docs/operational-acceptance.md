# Open operational acceptance gates

These are tracked requirements. Implemented offline primitives are distinguished from **unqualified live operational features**. No live backup, restore, scheduling or credential change is authorized by this document.

## Consistent SQLite backup and fail-closed restore

Current status: offline library primitives and local tests implemented; live recovery qualification remains open. See [supported APIs and procedure](backup-restore.md). WAL durability does not make copying only an open `.db` file safe. No backup/restore CLI is shipped.

Required implementation and qualification:

- [x] Use SQLite's consistent online backup/snapshot API, including WAL contents, rather than an ordinary file copy. Keep credentials and external encryption keys out of the ledger backup.
- [x] Write into a private temporary artifact, verify integrity and ownership/schema, then install a NEW destination atomically. Refuse accidental overwrite; preserve the prior database on interruption or failure.
- [x] Make every backup artifact and restored database admission-blocked. Clear/consume reconciliation nonces so an old or already active collector cannot reopen it. Preserve all reservations, dispatch intents, unknown resources and audit history.
- [ ] Require old gateway writers to be stopped/fenced before restoration. A database restore cannot revoke another process's external credentials or cancel remote work. Never treat the old process disappearing or a request timeout as resource absence.
- [ ] Require a NEW reconciliation barrier and authoritative fresh Garm, Incus and in-flight operation inventory after restore, including resources/creating requests absent from the older backup. No fresh dispatch claim may be issued before this completes.
- [ ] Do not resume live scheduling merely because SQLite integrity passes or the old snapshot used to be ready. Unknown or unbounded allocation remains blocked/charged.
- [x] Test concurrent writes during backup, incomplete artifacts, interrupted restore, destination preservation, artifact readiness, stale-fence rejection, post-backup orphan/creating resources and dispatch refusal until complete reconciliation.

Operator sequence using the offline APIs, before a live workflow is qualified:

1. Establish a maintenance window and stop/fence every old gateway writer. Keep external ambiguous operations visible and charged.
2. Take a consistent private SQLite backup with the supported tool. Verify integrity, schema/application ownership and blocked readiness; retain the original database and the required external key/configuration versions separately.
3. Restore to a new private path without overwriting the original. Verify that old collector fences and admission readiness cannot be reused.
4. Start a new reconciliation generation and collect authoritative complete bounded inventory from all three sources. Resolve unknown placement/operations conservatively, never by a timeout-based release.
5. Resume scheduling only after the new generation succeeds and the credential-isolation/host-budget boundaries are re-established.

The current repository cannot yet carry out that full sequence against infrastructure. The offline copy/restore boundary is implemented and locally tested. Proving a complete live recovery workflow with old-writer fencing, physical allocation coverage and fresh three-source inventory remains an explicit acceptance gate.
