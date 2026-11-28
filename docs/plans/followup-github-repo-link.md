# Link a new GitHub repository from the controller

Status: draft implementation plan. This document is not an implemented or activated feature.

## Scope

Reuse the configured-repo picker, pool mapping (#128/#134), and disabled stock pool staging (#412). Support an operator linking a new repo and assigning its ordinary GARM pool to an existing shared budget.

## Requirements

- Resolve the authorized GitHub installation and immutable repository ID; preserve authentication boundaries and show permission or webhook prerequisites explicitly.
- Record exact reviewed GitHub/GARM identity and intended pool action before any mutation. Register the repo with stock GARM, then attach an exact existing pool or stage a reviewed new one. Do not claim success before verifying readback.
- Make duplicate links and retries idempotent. Recover after GitHub/GARM partial success or an unknown upstream outcome without duplicating hooks, pools or access.
- Apply the canonical budget membership workflow. Activation waits on verified allocator and stock compatibility; distinguish staged from active in the UI.

## Acceptance

Disposable GitHub/GARM fixtures cover renamed repo, existing registration/pool, insufficient authorization, lost reply, concurrent double link and a repo with no pool. No external registration or webhook changes during development.

## Estimate and release boundary

3–5 engineer-days under the existing credential and webhook model. Engineering estimate excludes rollout and access lead time. Build against disposable fixtures; no live infrastructure, GitHub installation, or production mutations are needed for this PR. Keep stock GARM and existing queue/runner ownership. Mark any unimplemented or unsupported path plainly; do not mistake a fixture or plan for active capability.
