# Stock GARM settings ownership and uncertain writes

For enrolled pools the intended product contract is that the controller owns
capacity fields; users configure desired policy in its UI. Fairness weights,
caps and warm-idle targets are controller policy, not GARM priority or actual
allocation. Planner output is not permission to provision. The runtime broker
integration is separate work.

Stock GARM's fingerprint-before-PUT is not remote compare-and-swap. Another
writer can change settings after the check, and a successful GET cannot prove
that an earlier timed-out PUT will not arrive later. Externally enforced settings
ownership and an independently atomic provider gate remain required.

## Current behavior

The durable journal blocks new writes after a pending or unknown operation,
including after restart. The management UI now distinguishes whether the server
has a receipt-resolution path for that exact pending intent:

- Stock/legacy writes have no such receipt contract. The UI explains that the
  intent remains blocked; it offers no fake "resolve from current settings"
  operation.
- Historical experimental conditional intents can advertise receipt resolution
  only with the client enabled, an original dispatch record, and current target
  scope. This does not enable the experimental client or require a GARM patch for
  stock operation.
- Resolution uses its own explicit credential input, cleared after submission,
  refresh and disconnect. Failed/uncertain resolution does not unblock editing.
- The existing authenticated resolution endpoint retains separate write
  authorization and refuses intents outside the current pool allowlist.

This change does not implement stock-GARM unknown-write recovery, automatic grant
transfers, settings-writer fencing or the previously restricted dispatch binding.
It does not remove the idle-minimum decrease guard: decreasing a desired minimum
is not proof that an idle runner cannot take a job or that its capacity is free.
