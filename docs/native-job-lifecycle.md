# Authentic job lifecycle correlation (schema23)

This is durable positive correlation, not a drain/retirement verifier. It never
changes reservations, deletes a guest, fences GitHub acceptance, or releases
capacity. A GitHub `completed` job can still have live guest descendants.

## Sources and binding

`BindNativeRunner` requires the opaque `stockjit.ProtectedBootstrap` minted by
actual configured GARM metadata and stock instance/pool reads. It recompiles the
original canonical payload against the envelope's exact v3 policy and body hash,
and rederives the CREATE-UUID-bound bootstrap file hashes. It binds the metadata
runner ID/name/repository URL, actual stock instance UUID/pool/provider/repository,
stock source identity and exact native domain/incarnation in one immutable audit
transaction. One GitHub repository URL + runner ID cannot bind another permit.
No raw JIT/credential bytes enter this journal. Binding transactionally checks
current immutable pool enrollment and freezes its revision: exact source,
repository, authority, execution template/resources and native domain/policy must
match. Reads validate that historical revision. This is enrollment checked at
binding, not a claim that the older broker grant stored a profile revision.

`GitHubClient.ObserveExactJob` performs a real authenticated GET for one explicit
job ID in the configured exact repository. Its opaque result records actual
runner ID/name, run ID/attempt, status, conclusion and timestamps. It refuses
missing/unassigned/queued jobs, redirects, ambiguous identity fields and failed
responses. The GitHub response is not a native identity assertion: the ledger
joins it to the independently authenticated immutable runner binding and exact
successful START incarnation. The optional `ObserveJobLifecycle` wrapper also
checks an in-memory authenticated metadata identity; restart polling does not
need an expired instance token because the immutable ledger binding survives.

Reference: GitHub REST workflow jobs, `GET /repos/{owner}/{repo}/actions/jobs/{job_id}`:
https://docs.github.com/en/rest/actions/workflow-jobs#get-a-job-for-a-workflow-run
Stock source contract: GARM commit154638445c3949c1958b01812f69d9a1e4d82684,
`apiserver/routers/routers.go` /instances/{instanceName};
`runner/pool/pool.go` persists JIT agent ID/pending state before provider Create.

## Durable uncertainty rules

Only positive in-progress/completed states are stored, at most one immutable row
of each state per permit. Job/run/attempt/runner identity cannot change; terminal
conclusion cannot change and terminal state cannot regress. Lost acknowledgement
allows exact read-only retry, not duplicate execution. Missing or contradictory
reads leave existing history and all charges unchanged. Migration closes
readiness, append-only triggers and reverse-audit checks protect startup/backup,
and supported restore readiness/epoch invalidation blocks old-lineage writes.
Arbitrary raw filesystem rollback or competing external owners are not solved.

## Deliberate remaining work

The caller must supply a job ID from an independently scoped demand/event source;
this is not exhaustive job discovery or proof of no pending job. Protected guest
acceptance fencing, trustworthy job completion despite untrusted same-UID job
code, native physical deletion proof, and capacity release remain absent. No
production gate is removed, and no live host activity is performed by this change.
