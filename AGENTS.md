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
