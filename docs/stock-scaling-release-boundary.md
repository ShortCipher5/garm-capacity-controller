# Stock GARM scaling release boundary

GitHub owns job queuing. Stock GARM owns runner creation, assignment and
retirement. This controller compares demand and capacity across pools. The
current optional maximum preview is advice, not a change to GARM or a fleet
quota. No fork or host filesystem choice is required by the preview.

## Requirements for a change candidate

1. Prove which physical host the configured stock provider can actually use
   for the exact pool. A configured set of eligible hosts is not that proof.
   If host A is full and the provider routes to A, spare capacity on host B
   cannot justify growth unless the provider can demonstrably route there.
2. Evaluate CPU, memory and disk against that host and the effective runner
   size, with operator headroom intact. A low pressure sample is a veto
   input, not proof another runner fits. Missing or stale evidence must not
   justify growth. Continue charging unknown resources.
3. Require two independent, fresh observations of the same source, scope
   and policy. Confirm one bounded step, with equal weighting by default.
   Pause on drift, conflicts or unconfirmed outcomes. A scale-down must
   preserve configured idle floors and must not evict a busy runner.
4. Treat a previously observed maximum as history, not a safe rollback
   target. Re-evaluate any inverse change with current evidence and bounds.
   After a restart, do not infer that an uncertain operation succeeded merely
   because the current setting happens to equal its target.

The first code slices validate proposal inputs and expose reasons when the
controller cannot trust a recommendation. They do not create a writer,
enforce a physical quota, or make the preview safe to apply. Activation
requires a separately qualified bounded stock API path and rollout; it must
not be inferred from previews, local checks, or a successful image build.
