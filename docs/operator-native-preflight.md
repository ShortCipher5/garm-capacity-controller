# Native prerequisite read in existing server setup

The existing firstboot target review can expose an optional deployment-approved native diagnostic recipe. The browser reviews the same pinned host/admin connection plus fixed diagnostic artifact/script hashes and separate diagnostic review digest. No arbitrary host, command, path, executable or credential is submitted.

The separate prerequisite-read checkbox and action submit only the target ID, reviewed diagnostic digest and explicit read confirmation. They never reuse installation approval, run automatically or retry. The separately preinstalled diagnostic binary is required; this action installs nothing. A 45-second browser deadline exceeds the backend's 30-second observation budget. Cancellation/late responses invalidate the view; errors require a fresh approved-target load.

Reports always remain `unqualified_no_activation`. Observed prerequisite checks, missing/unsupported checks and informational/unavailable entries are displayed as historical host-reported diagnostics, not readiness, current capacity, enrollment or execution authority. Even a report containing only observed prerequisites does not qualify production operation.

Frontend tests exercise the fixed request and no-install boundary; actual remote diagnostic transport and binary pinning belong to the backend prerequisite, not the mocked browser fixture.
