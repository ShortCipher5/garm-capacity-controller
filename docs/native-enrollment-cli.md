# Trusted local native enrollment

`capacity-ledger enroll-native --source EXISTING_LEDGER --identity-file PRIVATE_IDENTITY --config PRIVATE_OWNERS_JSON`

The private JSON document contains an `owners` array. Each entry supplies `endpoint`, `tls` (`ca`, `certificate`, `key` private file paths), `owner_client_leaf`, `owner_server_leaf`, and the complete immutable native `policy`. Host, controller, domain ID and policy digest are derived from that validated protected policy. Duplicate hosts/domains, mixed controllers, duplicate/unknown/case-folded JSON keys and malformed pins are rejected. The controller client certificate must be separate from owner roles.

The command reads the actual pinned owner identity endpoint and matches the exact native daemon tuple to the existing immutable host binding. Only then does the canonical enrollment transaction record the domain. A configured policy, JSON receipt or success boolean cannot stand in for the observation. Changed backend identity is refused, never rebound. Exact existing enrollment retries are idempotent.

## First host binding

The explicit `--bind-initial-hosts` mode is limited to a not-ready runtime ledger with no host bindings, reservations, inventory, grants, pool profiles or overhead debt. The configuration must cover every configured host. It atomically rechecks the inspected audit sequence, complete host set and all empty/not-ready predicates before opening the reconciliation barrier, obtains all actual owner observations after that barrier, and calls the existing complete-set host binding transaction before domain enrollment.

This mode cannot adopt a previously bound host or migrate charged/unknown work. It does not complete reconciliation, open readiness, grant capacity, invoke native operations, exempt charges or authorize release. A timeout or later failure can leave the barrier or complete host bindings committed; domain enrollment can be partial. The diagnostic reports only confirmed domain records from that attempt. Preserve the ledger and inspect its existing bindings; if initial binding committed, retry the same reviewed owner configuration **without** `--bind-initial-hosts`. Never erase the ledger to force a retry.

Independent runtime identity is mandatory. Backup artifacts and offline/legacy ledgers cannot be adopted as runtime state. Native launch/claim guards remain unchanged. This workflow supplies enrollment only; actual overhead refresh is a separate observation-and-recording workflow, and native production qualification remains independent.

Tests use disposable loopback mutual TLS and a real runtime SQLite ledger. They do not demonstrate real host containment or native operation qualification.
