# Daemon-alias regression and remaining physical-scope qualification

## Required service-boundary regression

`TestServiceDuplicateDaemonAliasesFailClosedBeforeDispatch` exercises the actual
service, private credential files, provider RPC and temporary SQLite ledger.
Two disposable mutual-TLS endpoints present the same authenticated daemon key
but different authorities, projects and reported names. Each endpoint independently
supplies valid root identity evidence bound to its actual verified TLS certificate.

The service must authenticate both observations and reject duplicate daemon
budgeting before accepting reservations or contacting a backend write route.
Failing startup is safe; continuing to serve authenticated reads with admission
blocked is also safe. Provisioning is explicitly enabled in this fixture, so the
result cannot be attributed to the default read-only switch. No real Incus server,
GitHub runner or user infrastructure is contacted.

The negative control on the pre-binding implementation accepted both reservations,
issued two physical create POSTs to the disposable fixtures, and left readiness
true with 2000 millicores / 2048 MiB / two runners allocated across two aliases.
Each alias respected its own 1000-millicore / 1024-MiB / one-runner budget. The
regression must pass with durable duplicate-daemon binding rather than relaxing
that safety assertion or increasing a physical budget.

## Not established by a passing daemon-alias regression

An authenticated Incus key identifies an observed daemon, not unique hardware.
Different Incus daemons with independent keys can run on one physical machine.
Copied keys on different machines conservatively collide. TLS identity does not
establish native resource isolation, prevent external administrator changes or
revoke another process's credentials.

Project-complete inventory is not whole-host inventory. A separate disposable
negative experiment started with one full-budget allocation in an unobserved
project. The configured project's empty collection reconciled successfully and
another full-budget create was admitted. Its ledger correctly charged its own
1000 millicores / 1024 MiB, while actual physical allocations reached 2000
millicores / 2048 MiB across both projects. No arithmetic or transaction race was
needed; the missing premise was authoritative physical scope.

Do not mark that foreign-project experiment resolved by key/origin deduplication,
project binding, a healthy root API response, or a successful standard test suite.
These protections can prevent known alias/scope changes but cannot discover every
unconfigured project, daemon or independent ledger.

Before physical-host enforcement can be claimed, separately qualify:

1. Unique physical registration linking every relevant daemon/project to its real
   machine, with no independently budgeted duplicate or unobserved allocation path.
2. Complete authoritative accounting for that scope, or independently enforced
   native parent budgets and exclusive provisioning authority. Unproven scope
   must not be reported as complete.
3. The exact image/profile/device and native CPU/RAM/PID/disk/swap boundaries,
   operating-system headroom and lack of host socket/control-plane credential
   access from untrusted runner work.
4. External writer fencing: a two-gateway/two-ledger restore drill must prove old
   credentials cannot actually create after recovery, before restored admission
   opens. SQLite integrity, a historical snapshot and operator acknowledgement
   are not credential revocation.
5. Immutable backend-resource evidence and job-safe physical retirement before
   destructive lifecycle or charge release is enabled.

No daemon-binding or fixture test authorizes a live rollout or proves these
remaining physical, lifecycle and recovery conditions.
