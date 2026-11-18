# Explicit saved-selection assembly

The concrete nativeassembly.Service accepts deployment-fixed recipes, an actual
nativeplanning.Service, and a target ID plus saved selection digest. No caller
paths, endpoint, private configuration or injected builder callback is accepted.
Each recipe fixes the full Binding, approved artifact checksum, private template
and protected output parent.

Build obtains the actual fresh protected selection through LookupSaved, checks
its configured association, and persists a consumed intent before invoking the
real offline builder. It supplies and rechecks the expected selection digest,
then opens the resulting files through nativeprovision.LoadBundle and computes
the actual fixed-target Plan. Exact manifest checks and a synced completion
receipt precede the bundle_created_not_reloaded report with selection, manifest,
plan and assembly digests. ActivationAuthorized is always false.

The typed Completed result exposes a fixed internal path and target to trusted
registry code, never browser inputs or runtime authority. Registry reload must
be a separate explicit reviewed action comparing every exact digest. This service
does not upload, swap services, enable units or contact the administrator endpoint.

## Uncertainty and historical inspection

Each selection uses a deterministic fresh output and exclusive immutable intent.
Failures preserve partial output and cannot rebuild or adopt it. An OS file lock
serializes processes; bounded enumeration refuses new work at 1,000 retained
entries. There is no automatic cleanup, release or capacity ledger. Protected
private ownership is required. Arbitrary trusted-root mutation or rollback is
outside the guarantee.

InspectCompleted is read-only: canonical intent/receipt bytes, fixed recipe and
actual reopened bundle/manifest/Plan are rechecked. It does not re-probe or demand
that the original 15-minute OS observation remains fresh. Historical configuration
can remain inspectable after restart without becoming fresh capacity evidence.
Missing/inconsistent receipts and changed bundle bytes fail closed.

## Test scope

Focused tests use disposable authenticated SSH, the real inventory Probe/audit
and selection Save path, the real offline builder and actual protected files.
They cover exact selected resources, historical inspection after source removal,
corruption, partial-output refusal, cancellation before effects and one winner
among concurrent attempts. Artifact fixtures are synthetic ELF headers and never
executed. No real host preparation, ZFS/cgroup qualification or release is claimed.
