# Saved policy-cap reconciliation disclosure

The existing Garm settings view shows the actual optional `policy_cap_reconciler` process status. Current reported state is presented first; a historical successful pass never overrides `blocked`. Counts describe the last recorded result, not cumulative totals and not necessarily the current attempt. Running may include waiting between passes, not an in-flight guarantee. Failed settings refresh explicitly marks retained status stale.

The repository policy form now warns that an enabled private managed cap service may asynchronously lower positive Garm pool maxima to lower saved caps. It no longer promises that saving cannot change Garm settings, and labels the action Save capacity policy. The service does not increase maxima, change minimum idle, drain runners or release charges; zero/below-minimum reductions are skipped. The policy page alone cannot establish service configuration or status; it links to the actual existing management status.

No service activation, restart, retry, journal reset or configuration endpoint is added by this frontend slice.

Per-pool outcomes, when supplied, describe the last reported pass and may
precede an in-flight attempt. They are bounded to the scoped targets considered
by that pass. `not_examined` includes targets left after an early stop or batch
limit. Missing targets are not evidence of success. Confirmed reduction means
the original settings operation was confirmed, not that a runner retired or
capacity became transferable. No retry, release or upward-write control is
provided.
