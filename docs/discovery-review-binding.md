# Server-side discovery review binding

The existing allowlisted, authenticated read-only Incus discovery now returns a
`review` containing a server-minted ID, profile ID, and SHA-256 revision of the
sanitized discovery JSON (before the review field is added). SQLite retains the
snapshot, its original observation start time, and any complete detected totals.
No endpoint, SSH password, certificate or private key is added to this storage.

The UI submits that review reference alongside the operator's chosen budget and
paused initial mode. In one BEGIN IMMEDIATE transaction the server checks the
exact revision/profile, newest observation for that profile, configured maximum
observation age, and unused state; then enrolls and consumes the review with audit.
A newer discovery, including an incomplete one, supersedes older reviews. An
older request completing late cannot supersede a newer observation. Reviews and
consumption survive restart. Duplicate host conflicts do not consume a review.
Missing, stale, superseded, changed or consumed references return 409 without
mutation. Refresh discovery and review the proposal again rather than retrying
blindly. Discovery's original start time is never refreshed at enrollment.

Browser-provided `detected` totals are now rejected (400). Manual API enrollment
without a review remains supported but records no detected totals. Both paths
start unhealthy and disabled/draining. Review enrollment stores complete server
observations for display only; it never turns those observations into a fresh
health check, reconciliation fence, transport binding or physical capacity proof.

The snapshot proves which server-produced proposal supported the operator's
configuration, not that the host still has free resources or that a browser
actually displayed it. Budgets remain operator-selected and advisory warnings
remain separate from atomic physical admission. A review is not a secret or
permission token: management authentication and same-origin checks still apply.

Remaining onboarding scope: secure dynamic connection management and trust,
credential lifecycle, whole-host scope/enforcement, server profile identity
binding, profile/scheduler integration, and live qualification. This change does
not SSH to hosts, install agents, persist credentials, enable hosts, or deploy.

Migration must be integrated in order after the broker-policy schema migration;
this branch's final schema version and PR dependencies document that ordering.
