# Contributor guardrails

This is an experimental control plane above Garm, not a replacement for it.

- Never access live Garm, Incus or GitHub runner infrastructure in tests.
- Never include credentials, host addresses, private repositories or user-specific infrastructure.
- Do not claim capacity enforcement from periodic Garm pool-limit changes.
- Every provisioning path must eventually pass one atomic, durable admission boundary.
- Unknown or failed resources remain charged until authoritative reconciliation proves absence.
- Never reclaim capacity on timeout, failed creation, missing list entry or controller restart alone.
- Existing idle runners can still accept jobs after admission is stopped. Drain is not merely disable-create.
- Keep simulation visibly separate from production integration. No live adapter is implemented yet.
- Run `go test -race ./...`, `go vet ./...`, and `gofmt -l .` before publishing Go changes.
- Keep pull requests focused and draft. Do not merge or deploy without authorization.
- SQLite tests require Linux + CGO + a C compiler. Use the pinned module and checksums; do not silently disable database tests.
- Keep all budget reads, reservation changes and audit writes in a single BEGIN IMMEDIATE transaction.
- Collect a new complete bounded inventory only after BeginReconciliation returns its durable barrier/fence. Never reuse a fence or pre-barrier observations.
- SQLite WAL backups must use SQLite's consistent backup/snapshot facilities or a clean closed/checkpointed database. Never advertise copying the main .db file alone as safe.
