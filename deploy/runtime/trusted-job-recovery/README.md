# Exact queued-job recovery observer

This is a read-only acceptance observer, not an end-to-end recovery driver. It
does not dispatch a workflow, add a runner, modify a repository, release a
charge, or infer retirement from GitHub job state. It uses two bounded GETs
to GitHub's run-attempt and attempt jobs APIs. Redirects and outbound proxies
are disabled. No secret, private API response, or raw identity is printed.

## Required trusted evidence

- A separately approved dedicated fixture repository and reviewed fixture
  workflow commit. Record the repository owner/name **and numeric ID**, run ID,
  attempt number, and originally blocked numeric job ID.
- Before observing queue blockage or retirement, document the independent trial
  declaration and observation window (1–300 seconds). Bind the original
  queued observation to the exact waiting job and the negatively admitted
  reservation. Preserve trustworthy timestamps. The observer checks ordering;
  it cannot independently prove when the declaration was written.
- Obtain an authenticated, independently qualified receipt that the identified
  prior reservation retired and eligible headroom for the waiting job was
  restored in the same resource/budget scope. Review its byte digest separately.
  Scope and distinct reservation digests must be independently reconciled.
  An arbitrary JSON fixture, timestamp or stock-model simulation is **not** a
  qualified receipt. This repository currently does not produce the necessary
  qualification evidence for actual stock GARM. Do not enable a required live
  check or claim an actual pass until there is an approved producer and scope.

A trusted orchestrator assembles the reviewed policy **outside the untrusted
checkout** with the following exact keys:

- `schema`: `recovery-policy-v1`
- `reviewed_sha`: exact reviewed controller commit (40 lowercase hex)
- `repo`: exact allowlisted GitHub owner/repository
- `repo_id`, `run_id`, `attempt`, `job_id`: positive JSON integers
- `run_head_sha`: reviewed fixture workflow commit (40 lowercase hex)
- `scope_digest`: reviewed shared-capacity scope (64 lowercase hex)
- `receipt_sha256`: exact reviewed receipt bytes (64 lowercase hex)
- `declared_at`: predeclared UTC timestamp with `Z` suffix
- `window_seconds`: predeclared positive integer (maximum 300)

The reviewed receipt uses these exact keys:

- `schema`: `capacity-retirement-observation-v1`
- `repo_id`, `run_id`, `attempt`, `job_id`, `scope_digest`: exact policy bindings
- `blocked_reservation_digest`, `retired_reservation_digest`: distinct 64-character lowercase hex
- `queued_at`, `released_at`: UTC timestamps with `Z` suffix

Policy provenance and receipt authenticity are prerequisites, not established
by a matching digest from the same untrusted producer. Never let a dispatch
caller supply arbitrary JSON paths, scope, or digests as workflow inputs
in lieu of independent review or a protected workflow environment.

## Invocation once qualified

Run only on a GitHub-hosted runner from a manually dispatched trusted protected
ref with the implementation checked out at the exact reviewed revision.
The job should use a protected environment with restricted refs/reviewers,
checkout the trusted triggering SHA with credential persistence disabled, and
receive an Actions read-only token restricted to the dedicated test repository.
Never include dispatch, repository Administration, or runner-registration
write access in the observation job's token.

```sh
timeout 30s python3 -B deploy/runtime/trusted-job-recovery/verify.py \
  --policy "$REVIEWED_POLICY" --receipt "$REVIEWED_RETIREMENT_RECEIPT"
```

Supply the scoped token only as `GARM_RECOVERY_ACTIONS_READ_TOKEN` in that step.
Do not expose it to fork PRs, arbitrary-ref checkouts, or setup/build steps.
The launcher should not run from a public PR, and this contribution deliberately
does not enable a secret-bearing workflow without approved evidence sources.

A pass means the exact original fixture job is assigned and started **strictly
after** the independently reviewed release time and inside the declared window.
No inference from a different started job or from a vacant slot. Ambiguous
same-second order is a block. It is not a claim about production or an assertion
that unrelated jobs are safe to proceed.

No-secret component tests can run in ordinary GitHub Actions:

```sh
python3 -B -m unittest discover -s deploy/runtime/trusted-job-recovery -p 'test_*.py' -v
```
