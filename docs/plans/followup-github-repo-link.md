# Link a new GitHub repository from the controller

Status: partially implemented; no live repository writes, webhook changes, pool activation or browser enrollment route.

## Implemented boundaries

- A read-only GitHub App [installation catalog](https://docs.github.com/en/rest/apps/installations#list-repositories-accessible-to-the-app-installation) reads fixed-origin bounded pages using one token snapshot and returns only approved immutable repository IDs and current names. A missing entry is not evidence of deletion or sufficient write permission.
- The new `garmmanagement.Manager.EnrollReviewedRepository` coordinator records reviewed forge ID, immutable GitHub repository ID, credential/webhook recipe revision and pool/budget revision in the existing single-writer journal before dispatch. It records the acknowledged GARM UUID before exact readback. Replaying a confirmed plan does not contact the upstream; an ambiguous or pending write never repeats the POST.
- `RecoverReviewedRepository` requires the journaled UUID and authoritative settlement for the exact operation, forge identity, repository and reviewed plan. Without an acknowledgment, the transaction stays blocked; never search by repository slug to recreate it.
- No stock client implements the privileged registration capability in this PR. Finish a qualified stock enrollment adapter (installation authentication, immutable repository verification, webhook/credential handling and operation settlement), protected UI/API review, existing-pool selection or disabled-pool stage, and shared-budget attachment. Never expose proxy URLs, arbitrary upstream requests or raw credentials to the browser.

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
