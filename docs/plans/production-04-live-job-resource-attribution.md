# Exact per-job resource attribution and sampling

**Plan 04 · Draft engineering handoff · Code and offline acceptance only**

Starting reference: [`65b7a33`](https://github.com/ShortCipher5/garm-capacity-controller/commit/65b7a33a1b0f00bc2c839e33c669b229d6660d4b). Recheck current `main` before implementing.

## Outcome
Tie CPU and RAM usage to the exact GitHub job attempt running in the actual Incus runner. Preserve coverage and scope, omit unverifiable metrics, and make the resulting history suitable for advisory sizing. No job proxy, webhook interception, remote shell, or added privileges.

## Start here
Reuse `internal/demandrepair/ci_runner_assignment.go`, `internal/garmapi/running_registration.go`, `internal/incusapi/runner_runtime_{health,summary}.go` and `internal/cihealth/{stock_job_resource_binding,resources}.go`. The stock registration read does not prove provider allocation, and an Incus read proves only its chosen subject. [Draft #377](https://github.com/ShortCipher5/garm-capacity-controller/pull/377) projects a sealed completed job to the reducer in simulation; it is not a live collector.

## Implement
1. Establish a fixture-backed source capability contract for exact GitHub source/repository/workflow/run/attempt/job and positive runner registration, exact GARM source/repo/pool/row, and independently verified original provider allocation to authenticated Incus project/member/daemon and immutable instance incarnation. Identify the process boot/start epoch independently. A name, mutable provider label, timing, or separate TLS on both reads cannot supply the missing join.
2. Implement a bounded read-only collector only for capabilities evidence actually supports. Bind before job start and at the cutoff or an independently verified stop; do not span job boundaries or stitch across runner reuse, reboot or counter resets. Positive historical completion does not prove resource counters covered the interval. A response arrival timestamp alone does not prove the job was still executing at that moment.
3. Use the existing reducers and preserve coverage, sample age/count, reason for omission, time-weighted RAM average and sampled peak, and CPU consumption/rate. Keep attributable disk usage/I/O separate; host free disk is not job usage. Do not infer per-step usage or treat external builder work as fully measured. Short jobs that were not captured remain unavailable.
4. Persist verified measurements via Plan 05 and expose exact attempt/job usage in the CI Health UI with stale/unavailable/partial states. Keep this read-only and separate from autoscaler authority. Plan 07 consumes verified cohorts only.

## Code acceptance
- Matching independently proven assignment and runtime with positive coverage produce the expected numerical values. Cross-repo/attempt/runner, absent origin, reused instance, reset counters, overlapping jobs and stale or unbracketed samples produce no unsafe attribution.
- Incomplete sampling shows coverage accurately; unsupported disk and short jobs are not shown as zero. No collector needs production shell access or new privileges to run offline tests.
- If no documented source supports the provider-origin join, deliver the capability matrix, validators, and explicit unavailable state only. Do not claim end-to-end live metrics in that case.

## Dependency boundary
No live access or new sources are required for the offline work. Activating real collection requires supported evidence of original allocation and process identity under the existing read scopes. Plan 03 shares the placement identity requirement but uses different terminal evidence.

## Handoff and definition of done
This draft contains the plan only. Take over this branch or split implementation into small reviewable commits/PRs referencing it. Identify the exact baseline and behavior already present, ship only the delta, and include offline automated regressions for the acceptance cases. Keep flags off where capabilities are unavailable, document rollback of the code change, and report outstanding dependencies explicitly. No live server changes, new access grants, production rollout, or operational rehearsal are assigned. See [original scope](https://github.com/ShortCipher5/garm-capacity-controller/issues/53) and [CI Health scope](https://github.com/ShortCipher5/garm-capacity-controller/issues/268); this plan and current stock-first code take precedence over older experimental notes.


### Offline validation boundary

The consistency validator requires an independently approved mapping from the
exact GitHub source and immutable GitHub repository ID to the exact GARM
enrollment. Equality of runner agent IDs or repository labels cannot supply
that mapping. The validator does not create or authenticate an enrollment.

For direct verification, the independently observed row, job execution and
closing bracket must each be current within the maximum sampling gap. The
latest actual supporting observation is shown; a new render time or declared
window time cannot refresh old evidence, and no source observation is shown
as unknown. Historical retention needs a separate verified history owner.
This does not wire a producer or enable stock live per-job resource
collection.
