# Guarded retirement automation

`nativecontroller.RetirementService` scans immutable runner bindings in bounded
64-row pages. A recorded root-fence/terminal-job receipt selects a candidate,
not release authority. Exact configured domain and policy are required. The
real Coordinator independently obtains fresh pinned owner evidence before
recording an intent, then uses canonical one-use STOP and DELETE claims.
The existing positive read-only job observer is not granted mutation authority.

Cursor state is fairness only; a restarted service can safely begin at the
first row because permanent claims and accepted/terminal histories govern each
operation. Lost replies lead to read-only recovery, never renewed permissions.
Concurrent cycle calls serialize with cancellable waits. Run cancellation joins
the active cycle. Unknown outcomes retain the full reservation.

OperationsCompleted means STOP and DELETE history succeeded, NOT that the host
is empty, every extent is gone, or capacity was released. ReleaseAuthorized is
always false. Genuine namespace/storage observations do not yet bound historical
shared extents and host-global kernel/cache residuals. No physical-completion
producer or canonical refund is fabricated. The denied nested-accounting/binding
work is neither copied nor routed around.

Actual runtime stores remain refused before owner access by both service and
coordinator. Real loopback TLS and SQLite integration uses an explicitly
simulated owner protocol, including STOP/DELETE lost replies, unknowns and
cancellation/restart. It does not qualify native containment or deployment.
