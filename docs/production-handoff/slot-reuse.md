# Production implementation plan: safely reuse retired runner capacity

**Status:** Design and acceptance criteria only. No activation, deployment, host change, or release of a charged runner is authorized by this plan.  
**Baseline:** `fb8c39c26b865cc277783d8899af32dd63e6a836`. Follow-up to [#53](https://github.com/ShortCipher5/garm-capacity-controller/issues/53), [#380](https://github.com/ShortCipher5/garm-capacity-controller/pull/380), [#387](https://github.com/ShortCipher5/garm-capacity-controller/pull/387), and [#391](https://github.com/ShortCipher5/garm-capacity-controller/pull/391). Recheck the exact implementation base before coding.

## Outcome

A stock-GARM runner that naturally finishes and retires can give its slot back to the shared limit **once**, after independently verified evidence proves that the corresponding work is settled and that its charged physical resources are no longer held. When that evidence is missing or inconsistent, preserve the charge and show operators why. A reduction in a pool's configured maximum does not retire a runner or release capacity.

GitHub continues to queue and assign jobs. Stock GARM and its existing provider own runner creation and retirement. This controller observes and accounts; this work does not fork GARM, add a second runner lifecycle, standardize everyone on one filesystem, or authorize a STOP/DELETE/deregister call. Do not add or repurpose a dispatch-envelope/journal/API/ledger binding. Do not use a live guest, job token, credential, or test access path as a substitute for an approved evidence producer.

## What already exists

| Source at the baseline | What to reuse and what not to assume |
| --- | --- |
| [`internal/admission/release_linux.go`](../../internal/admission/release_linux.go) | `ReleaseConfirmed`, `ReleaseEvidence`, and release tombstone machinery exist. Review the active guards and preserve them. Do not use the legacy entry point, delete accounting rows, or supply synthetic proof to bypass the hold. |
| [`internal/admission/native_retirement_linux.go`](../../internal/admission/native_retirement_linux.go) and [native retirement ledger](../native-retirement-ledger.md) | Native retirement intent/claim/receipt machinery is not a stock-GARM release source. Even a recorded successful native DELETE does not refund physical capacity. Native activation and mutations are outside this plan. |
| [`internal/incusapi/retirement_observation.go`](../../internal/incusapi/retirement_observation.go), [physical proof boundary](../physical-retirement-proof-boundary.md), [safety hold](../retirement-safety-hold.md) | `ObserveProjectAbsence` provides scoped diagnostics with explicit blockers. A matching or empty project view cannot rule out hidden scope, a competing writer, surviving storage or unfinished work. |
| [`internal/adminview/native_retirement_reuse.go`](../../internal/adminview/native_retirement_reuse.go), [`internal/admission/capacity_state_view_linux.go`](../../internal/admission/capacity_state_view_linux.go) | Existing output separates historical/incomplete diagnostics from release authority. The charged snapshot reports disagreement without inventing another charge or refund; retain that behavior. |
| [Stock release boundary](../stock-scaling-release-boundary.md), [post-delete diagnostics](../retirement-diagnostics-consumer.md) | Preserve the original provider route through cutover/rollback while work remains. A verified delete outcome and a retained-observation report are distinct milestones, neither is sufficient alone. |

## Implementation sequence

### 1. Specify and test evidence without a live dependency

Before solving collection, write an internal, source-versioned contract and table-driven classifier for these facts. Test data can exercise the contract; production must not accept a caller-constructed assertion, a manual check box, or raw status strings as evidence.

- **Original identity:** exact enrolled GARM source/controller and pool, original provider route, immutable Incus daemon/project/instance incarnation, and the exact ledger hold at that scope/epoch. Preserve the original owner across changes to pool mappings, provider settings, or instance name reuse. Capture the actual allocated resource scope, not a guessed profile or a current object with the same name.
- **Job settlement and no recreation:** trustworthy terminal state for the exact accepted work and finished worker/post-job activity; independent closure of new assignment and old start/retry/reconnect work for that allocation. For the first candidate, limit scope to naturally exhausted, one-job ephemeral runners under a qualified source contract. Idle, offline, stop requested, terminal webhook alone, and registration missing do not establish settlement. A runner that might still accept work stays held.
- **Original teardown and residuals:** independently verified outcomes linked to that original physical incarnation and its owned storage, any applicable held processes, and qualified evidence that relevant resources have settled. Record only facts the exact pinned provider and storage backend can prove. Do not infer durable operation history from a late list or event, or retroactively re-create a missing original operation identifier. Unknown storage or visibility stays unknown. No mandatory ZFS assumption and no use of host-free bytes as proof of reclamation for a specific hold.
- **Independent fence and scope:** prove that outstanding accepted work or an old writer can no longer change the conclusion, with scope and generation compatible with the evidence above. A current match, matching readback, or process/listener restart does not supply that fence. Keep source version, epoch, freshness and invalidation rules explicit and auditable.

Return separately: `unsupported_source`, `awaiting_job_settlement`, `awaiting_teardown_evidence`, `awaiting_storage_evidence`, `fence_unproven`, `stale_or_conflicting_evidence`, and “candidate evidence complete.” Names may align with existing repository conventions. Status is diagnostic and must not itself permit a release.

### 2. Qualify each source and publish its capability limits

Use docs, existing authorized observations, or disposable fixtures; distinguish source-level expectations from observed backend behavior. Identify for each fact whether the supported interface exposes it, which principal and exact version can observe it, and what makes it invalid. In particular, establish whether ordinary pinned stock GARM plus the installed provider exposes original teardown identity and independent closure at all. The upstream pattern that reports success for an already absent object is not by itself proof that the original object was removed; a cleanup row disappearing is not a replacement receipt. If any required fact is not supported, return an explicit unsupported reason and leave the stock scope charged. Investigate documented extension points separately rather than substituting unauthorized access or imitating a trusted source.

**Pre-implementation decision gate:** can the exact stock/provider/Incus/storage combination give affirmative evidence for all four boundaries above? If not, submit only the disabled verifier, proof contract, and diagnostics/tests. Do not unblock automatic release by reducing confidence or introducing another lifecycle implementation. Document which missing proof would require a supported upstream/provider change.

### 3. Implement the guarded accounting transition only after qualification

- Use one existing authoritative accounting store and transactional update for the exact original hold. Recheck evidence scope, reconciliation epoch, history and fence validity at commit; a prevalidated recommendation is not authority. Preserve monotonic lineage and exclude terminal records from new mutations.
- Make identical evidence replay return the prior decision with **zero additional credit**. Mismatched replay, new incarnation under the same name, multiple workers, or existing contradictory inventory must preserve the charge or trigger a hold. A cancelled/never-dispatched identity remains on its existing separate path.
- First positive release scope should be all-or-nothing for the resources represented by the charged runner. If product work later permits partial credit, define separate resource states, prove each independently, preserve residual charges and exact linkage across restart, and qualify both evidence orders before enabling it. Never advertise partially released storage as a free runner slot.
- Expose only sanitized hold category and aggregate accounting status through existing read-only diagnostics. Preserve distinctions among charged, measured, and eligible-to-reuse capacity. The shared-cap plan consumes the **committed ledger result**, not the precheck, dashboard or readiness label.
- Default off, preserve current read-only/hold behavior for all unqualified scopes. Review persisted schema and downgrade compatibility before any format change. Preserve tombstones, held counts, carried evidence, and pending records across supported reopen. If rollback cannot read the resulting state without losing charges or replay protection, keep the feature disabled until a migration/rollback contract exists. Stopping the feature closes further releases without rewriting past accounting.

## Verification and acceptance

| Layer | Required cases |
| --- | --- |
| Offline classifier | Full positive synthetic case and each missing evidence fact; same display name/different incarnation; wrong controller, pool, daemon, project, route, storage or epoch; stale/out-of-order/partial evidence; hidden scope or truncated lists; stock row disappearance and provider success/404 are not enough. |
| Store/restart | SQLite reopen at each precommit boundary, failed/cancelled transaction, exact duplicate, different evidence on duplicate, parallel readers/writers, and reappearing or conflicting inventory. Assert resource/count vectors before and after, not just status strings. No debit/credit on failure. |
| Integration | Disposable read-only operator view identifies reason without credential, raw token, or unsafe fallback; shared-cap consumer sees only durable authorized credit; cancellation, timeout or provider/ledger disagreement never grow available capacity. Existing hold and disabled-runtime tests stay green. |
| Qualification | For any approved, isolated ordinary stock-GARM run, verify exact provider/Incus versions, original route and exact job/runner, natural completion, original teardown, retained storage and cleanup. The existing one-job Mumbai smoke may furnish evidence for its approved scope only; it proves neither all backend behavior nor sustained recycling. Broader lifecycle or fault tests require their own isolated authorization. No host action or workflow dispatch is part of this PR. |

Required sign-off: exact base and merged-tree CI, independent accounting and operator-safety review, a reproducible list of proven/missing sources, and separate rollout approval. Disposable tests can qualify the code's fail-closed behavior but cannot replace affirmative backend proof. If the proof does not exist, no release.

## Sibling plan boundaries

- **Operator stop and recovery** owns paused/unresolved stock settings writes and re-entry. A healthy operator listener or a matching settings read is not physical-release evidence.
- **Enforced shared capacity across repos** owns admission policy and uses exact committed accounting; it cannot release slots using a demand snapshot or an apparent reduction in runner count.
- **Live per-job resource monitoring** may share an approved immutable allocation identity; sample counters and historical usage still do not establish assignment closure, teardown or reusable resources.
