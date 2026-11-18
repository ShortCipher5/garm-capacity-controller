# Positive job and root-journal observer service

`jobobserver.New` binds a concrete ledger, authenticated GitHub client, exact
repository catalogue and frozen pinned-owner TLS/policy configurations. `Run`
repeatedly executes bounded cycles. Every cycle pages immutable runner bindings,
uses durable known job IDs directly after restart, or enumerates configured
recent/active GitHub runs for candidate IDs. It then re-reads the exact job and
persists only positive matching runner/repository/incarnation evidence.

After a recorded terminal job it performs the real owner journal GET and records
the schema24 root closure receipt. Missing, changed, unavailable, partial or
unclosed results remain unresolved; no inferred absence or automatic cleanup.
Successful writes are individually durable and idempotent across interruption.
An in-memory keyset cursor only provides fairness, never authority or completion.
Old unseen completed workflows outside the configured lookback can remain
unresolved; the service does not claim exhaustive history. Durable known job IDs
are retried regardless of that window. No resource mutation or release occurs.

Gateway wiring is opt-in through `job_observer` configuration plus
`--allow-job-observer`. It requires the existing separately authenticated admin
listener. Repository catalogue, GitHub endpoint/CA/token file and exact native
owner policy/TLS credentials are deployment-owned configuration, not browser
inputs. The service begins only after gateway startup/reconciliation, stops and
joins before the database/client closes, and retries from durable lineage after
restart. `/admin/api/v1/job-observer-status` returns actual sanitized cycle
counters and unresolved state; release authorization is always false.

Existing native creation/runtime guards remain unchanged. This observation loop
can only append authenticated history; enabling it cannot enable owner mutation,
provider provisioning, retirement or release. Native image/host runtime remains
unqualified, and no live observer was started during development.
