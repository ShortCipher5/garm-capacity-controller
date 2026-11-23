# Advisory runner sizing from verified history

**Plan 07 · Draft engineering handoff · Code and offline acceptance only**

Starting reference: [`65b7a33`](https://github.com/ShortCipher5/garm-capacity-controller/commit/65b7a33a1b0f00bc2c839e33c669b229d6660d4b). Recheck current `main` before implementing.

## Outcome
Use reliable job history to recommend smaller, larger or unchanged runner profiles, and explain cost and headroom tradeoffs. Never use a recommendation to write pool settings, assume free capacity or reduce a guaranteed allocation.

## Start here
Reuse `internal/cihealth/{job_sizing,job_sizing_window}.go` and `docs/ci-health-evidence.md`. The conservative calculation is a foundation. Its inputs are not verified live just because fixtures pass.

## Implement
1. Build an explicit eligibility policy for comparable cohorts by trusted workflow/job/matrix identity and relevant profile or revision. Supply the existing calculator with exact resource records from Plans 04–05, sample coverage, age, count, risk and outcome; no joining stale or unverifiable identities. Avoid interpreting a sampled peak as a complete upper bound.
2. Bring the calculator into the read-only dashboard: eligible sample count, coverage, observed CPU/RAM, recommendation and uncertainty, current runner profile, host budget/headroom context, reason for no recommendation. Warn about sustained pressure and insufficient spare capacity; never hide a hard fit failure behind a warning. Only include disk advice where attributable data actually supports it.
3. Use conservative floors/headroom, suppress oscillation and downgrades with insufficient current data, and keep suggestions separate from persistent user settings. Preserve neutral “no recommendation” for low coverage, heterogeneous cohorts, reset/missing values, unreliable external work and unsupported dimensions.
4. Track outcome and cohort policy version so a recommendation is reproducible with the exact source facts. No pool application, cap changes, job routing or delegated operation exposed from this path.

## Code acceptance
- Comparable exact histories produce deterministic qualified recommendations with source age and sample coverage. Cross-tenant, sparse, stale or incomparable inputs cannot produce confident downsizing.
- Distinct matrix cohorts and changed runner profiles do not get silently combined. Genuine low usage is distinct from missing samples.
- The feature leaves all scaling settings unchanged and has no apply action. Local fixtures can verify the model and view without host or GitHub access.

## Dependency boundary
Use `cihealth.JobSizingSample` in `internal/cihealth/job_sizing.go` as the shared Go contract: Plan 04 produces a verified `JobResourceSummary` with its exact `JobKey`, runner incarnation, observation window and coverage, Plan 05 retains that summary and the independently sourced completion, installed shape, outcome and risk facts under the same key without refreshing their timestamps, and Plan 07 projects them into `JobSizingCohort` for `ReviewJobSizingWindow`, treating missing facts as unknown.

Can implement policy/view with fixtures in parallel. Live recommendations remain disabled/unavailable until Plan 04 has verified resources and Plan 05 has durable exact history. This is post-scaling functionality, not a condition for safe scaling code to be reviewed.

## Handoff and definition of done
This draft contains the plan only. Take over this branch or split implementation into small reviewable commits/PRs referencing it. Identify the exact baseline and behavior already present, ship only the delta, and include offline automated regressions for the acceptance cases. Keep flags off where capabilities are unavailable, document rollback of the code change, and report outstanding dependencies explicitly. No live server changes, new access grants, production rollout, or operational rehearsal are assigned. See [original scope](https://github.com/ShortCipher5/garm-capacity-controller/issues/53) and [CI Health scope](https://github.com/ShortCipher5/garm-capacity-controller/issues/268); this plan and current stock-first code take precedence over older experimental notes.
