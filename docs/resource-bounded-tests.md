# Resource-bounded contention tests

The admission subprocess tests keep **twelve total contenders**, run in three
waves of **four simultaneous contenders**. They no longer claim twelve-way peak
process concurrency. Every wave opens the shared ledger, reports readiness, then
waits for one atomically published start gate immediately before its operation.
The parent requires all four ready and alive before release, then waits for every
exit before starting the next wave.

The JSON and SQLite tests still require exactly two admitted reservations across
their global budget. The dispatch test still requires exactly one backend call.
Cancellation still races both claim and cancel and requires exactly one winning
outcome with the correct charge. Existing goroutine and independent-handle races
are unchanged. All twelve outcomes still participate in final replay/budget checks.

Each helper explicitly uses and verifies `GOMAXPROCS=2`, rather than inheriting a
large host's scheduler width. Barriers, child commands and cleanup are bounded.
A negative control deliberately launches processes that never join the barrier;
even their successful status-zero exits must fail the control driver.

For a resource-constrained CI job, bound package and testing-library parallelism
as well (these do not disable concurrency exercised inside each test):

```sh
GOMAXPROCS=2 go test -race -p=1 -parallel=1 -count=1 ./...
go vet ./...
```

This is test orchestration, not a change to runner CPU/RAM/PID limits or production
admission policy. Do not raise a runner's process cap to hide a failing test.
Passing on a developer computer does not prove that a particular self-hosted
container's process/resource/isolation envelope is qualified. Rerun the actual
trusted canary with its original limits, verify all required gates and cleanup,
and keep permanent CI migration disabled until that succeeds.
