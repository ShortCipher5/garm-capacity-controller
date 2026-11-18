# Allocator service lifecycle (no production activation)

The concrete brokerinput Service owns serial observe/plan/issue cycles through
the existing GrantCycle implementation. It reports actual attempt, completion,
success and stop events. Configuration, existing grants and saved policies never
fabricate a successful cycle or a running loop. Errors expose only bounded
reason codes; each attempt clears the preceding result's batch/count first.

The service can exercise supported non-runtime ledgers with a real DemandClient.
A runtime-lineage ledger is rejected before the first cycle, even with enrolled
profiles, healthy host observations or a configured managed proxy. These are not
a native execution/management activation capability. No override boolean exists.

Gateway configuration may request lifecycle supervision:

    "allocator_loop": {"interval_seconds": 60, "grant_ttl_seconds": 300}

Both values must be 60–3600. The gateway's actual runtime store currently records
a blocked startup with reason native_management_authority_unavailable and zero
attempts. It does not launch a live grant writer. Omitting this configuration
leaves the service unconfigured. Status is process-local and resets on restart;
the durable ledger, not these counters, remains the accounting authority.

Remaining activation code: genuine native execution boundary plus managed
settings authority, managed demand provenance/enrolled source consistency, and
their coordinated startup/revocation. This lifecycle/status slice does not
implement those or imply deployment.

The authenticated GET /admin/api/v1/allocator-status uses the existing admin
read token and reports process-grant-loop scope. The overview's optional Watch
control polls every 15 seconds only while open and visible; it triggers no
runtime actions. Failed reads remove the previous snapshot. Browser interaction
regressions are provided for hosted CI; no local browser qualification is claimed.
