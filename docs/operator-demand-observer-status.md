# Read-only backlog observer in the existing policy screen

An optional gateway `demand_observer_config_file` loads the same protected,
explicitly enabled configuration used by `capacity-demand-observer`. Admin
configuration is required. No browser credentials or arbitrary endpoints are
accepted by this feature. Startup freezes configuration and runs one sequential
read-only observer, joining it on gateway shutdown. Existing transport scoping,
TLS, protected credentials, checkpoint locking and failure backoff are retained.
Only the observation checkpoint is written; no allocator, grant, pool-setting,
runner or resource-ledger capability is supplied.

Authenticated GET `/admin/api/v1/demand-observer-status` exposes bounded summaries
in the existing repository policy area. Stock queued count, merged observation
count, current reader-only items, retained history, conflicts and missing-reader
flags are distinct. Conflicts/missing flags overlap provenance categories and
must not be added as separate demand. Retained history is not current GitHub
confirmation. Successful enumeration is neither an atomic snapshot nor a promise
of complete missed-webhook coverage.

Expiry is anchored to poll start, at twice the configured interval capped at five
minutes. Failed, stopped or expired observations never return current counts.
The UI independently expires its captured result and does not restore a dismissed
or disconnected request. Source category and timestamps describe provenance; raw
endpoints, repository/job identifiers, tokens and checkpoint contents are omitted,
as in the existing standalone observer summaries. This does not feed repaired
demand into allocation or authorize any runtime action.
# Read-only demand observation status

The existing allocation/policy console can read the configured observer's status with the normal admin read session. Opening the panel only issues fixed GET requests; it cannot start an observation, change configuration, write the ledger, feed the allocator or authorize provisioning.

Stock queued jobs, the merged observation union, GitHub reader-only items, retained history and overlapping conflict/missing flags are shown separately. Never sum these categories into a dispatch target. Stock-reported runner states do not prove physical capacity or completion. Only source category and capture timestamps are displayed; raw source identities and digests are not disclosed. The sources are non-atomic and full missed-webhook coverage is not established.

The panel refreshes status every 15 seconds while open and visible, and hides counts at their recorded expiry even before the next read. Unavailable responses remove the prior view. Closing, disconnecting or aborting fences late responses; no write, reset or retry action exists.
