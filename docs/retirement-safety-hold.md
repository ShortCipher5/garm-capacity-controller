# Physical retirement safety hold

Physical runner retirement is not yet implemented safely for the stock-GARM
capacity-broker architecture. `Facade.Delete` must not issue a destructive
Incus call, even when provisioning is explicitly enabled. Exact previously
cancelled, never-dispatched identities retain their existing no-resource replay.
An existing release tombstone does not authorize a new physical action.

## Why the hold exists

Two matching authenticated project-scoped reads are useful observations, not a
lease on absence. Their typed record explicitly retains blockers for external
writers, job assignment/completion, authorization visibility and whole-host
coverage, original deletion history and the post-delete barrier. The ledger
release validator now rejects observations with those blockers. It does not
turn a deleted flag, timeout, successful HTTP reply or fresh reconciliation into
release authority.

The earlier deletion flow verified an incarnation and then deleted by name
without a held incarnation or job fence. It also lost the ability to recover
when a process died after deletion but before ledger release. Retrying that flow
was not a qualified production recovery protocol. It is removed rather than
exposed behind an operator acknowledgement.

## Implementation still required

The durable stock-GARM design must establish an enforceable assignment and
completion boundary, exclusive physical writer/incarnation ownership, durable
delete intent and operation history, and recovery of the exact original operation
without issuing a new destructive action on an uncertain outcome. Only then may
a separately reviewed finalizer retire the charge. Host control and stock-GARM
interfaces must supply real authority; a timestamp or manual boolean cannot.

For now, pending/deleting/unknown charges stay allocated across restart. The
regression suite verifies that qualified transport observations alone do not
release them and provisioning opt-in cannot enable deletion.

This is a safety correction, not completion of retirement, automatic recovery,
capacity recycling or deployment qualification. Existing releases written by the
previous implementation are not silently reversed or rewritten by this change;
any installation that used that path needs an explicit accounting and physical
inventory review before scheduling further work. No live installation was
inspected or changed while implementing this correction.

## Offline recovery assessment

`capacity-ledger assess-recovery --source /private/ledger.db --limit 100`
reads an existing current-schema ledger without opening a writer, changing
readiness, migrating, or accessing any host. It reports reservation identity,
dispatch phase, linked inventory count, historical-release status and the
safe next investigation step. It does not print dispatch permits or bootstrap
material. Treat the output as private operational data.

All reservations are included, even confirmed creates: a historical success
is not current resource identity, idleness or absence evidence. `ledger_charged`
means the current ledger has not excluded that reservation through a release;
it is **not** measured physical resource use. Historical releases are flagged
for authoritative inventory/accounting review, not silently reversed. Cancelled
never-dispatched requests have no reservation and are not recovery work.

For another page, pass `--after` using `next_after`. Each page is one read-only
snapshot, but multiple pages are not a consistent inventory if writers are
active. Neither an empty report nor `dispatch_state=confirmed` authorizes
resume. `resume_authorized` is always false. This assessment cannot inspect
post-backup work missing from the database or validate that another writer was
fenced. Backups/restores remain runtime-disabled; no activation flag is added.

The separate missing implementation is an authority-backed recovery coordinator:
exclusive old-writer control, complete post-barrier physical inventory,
incarnation-bound job-safe retirement, durable original deletion-operation
recovery, and a qualified capacity-release finalizer. Stock GARM and an external
provider gate remain the intended architecture. No custom GARM patch, operator
JSON assertion, timeout, missing list entry or restart is substituted for those
boundaries by this diagnostic primitive.
