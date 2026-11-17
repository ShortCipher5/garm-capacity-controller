# Host onboarding: budget guidance slice

This change adds advisory planning to the existing, reviewed, disabled-host enrollment form. It does not add SSH connection setup, install an agent, activate hosts, or make physical-ownership claims.

The starting allowance is 10% of detected totals, with minima of 1 vCPU, 2 GiB RAM and 10 GiB disk, capped at total capacity. These are product defaults, not a workload guarantee. Existing observed guest CPU/RAM limits are reserved conservatively even when attributed. Disk allowance considers the sum of existing root-disk limits and observed used disk (deliberately double-counting any overlap because per-guest used-byte attribution is unavailable), plus headroom; a reported storage pool further bounds it. This deliberately may underutilize a host rather than double-book its known workloads. Existing ledger allocation import and whole-host scope enforcement remain separate prerequisites.

Exhausted disk never prefills an unconstrained zero: it stays blank with a no-capacity warning. Incomplete totals, guest inventory/limits, or disk usage do not become zero: the affected proposed budget is blank and requires manual review. The current backend omits zero disk usage as well as unavailable usage; this UI conservatively treats either omission as unknown. Warnings explain edits above the proposal and zero disk's unconstrained semantics. Normal, in-range budgets do not receive a low-headroom warning, but discovery scope limitations remain visible.

Small/medium/large planning presets show independent resource-only maxima and a combined editable count. They are not automatically saved profiles or worker reservations. Existing profile management remains the supported place to save custom sizes. A separate warning identifies a mix above the host's runner-count limit.

## Remaining full onboarding work

- Secure Add server connection enrollment with SSH host-key verification or verified Incus trust, credential lifecycle, explicit installation review, and revocation. This slice neither accepts nor stores SSH secrets.
- Determine supported Incus scope/permission enforcement before introducing a minimal agent. An agent is justified only for required visibility/enforcement unavailable through supported APIs, not merely for displaying hardware totals.
- Versioned reviewed discovery/budget binding and server-side advisory validation, including stale observation handling. Client guidance is not authorization.
- Integrate configured profile selection and mixed workloads with durable scheduler grants; never reinterpret planning counts as permission to provision.
- Continue existing safe activation gates, authoritative inventory and retirement/recovery requirements.
