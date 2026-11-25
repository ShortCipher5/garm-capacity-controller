# Disposable read-only supervisor recovery acceptance

This is an acceptance protocol, not a deployment script or a claim that a real
managed topology has passed. It does not authorize host contact, credential
provisioning, stock mutations, container restart, or native activation.

## Hermetic checks available now

Run `go test -race ./internal/managedwriter ./internal/managedgarm
./internal/brokerinput` (as one shell line). Relevant contracts:

- `TestWriterSuspendWaitsForActualHandlersAndDoesNotTerminateOwner`: real
  loopback HTTP drain; failed suspension does not report a stopped service.
- `TestReadOnlyOverAuthenticatedTLS`: actual disposable TLS and the fixed GET
  settings handler and the Service listener lifecycle, with a synthetic stock
  backend. The test holds a second authenticated GET inside the handler,
  witnesses a timed-out suspension, releases the read, and confirms successful
  drain and closure of the old listener. A canceled Resume leaves the service
  stopped with its original journal lineage and pending intent set. Mutation
  requests and an unauthenticated client remain denied.
- `TestFailedExplicitResumeRetainsReadOnlyAndJournal`: failed explicit Resume
  retains the read-only option and same durable journal, exposes no replacement
  listener/session, and does not fabricate termination or mutation intents.
- `TestInterruptedTopologyCheckPermanentlyRevokesSession`: cancelled/expired
  topology checks latch revocation; fresh caller contexts cannot revive it.
- Advisory loop tests cover in-flight cancellation, expiry, and removal of a
  previous cached plan after a failed refresh.

These tests must not be joined by assigning a fabricated successful Session or
injecting a replacement authentication callback into production. They do not
prove successful protected Resume.

## Hosted disposable fixture sequence (not executed here)

Prerequisites: a separately approved disposable managed topology passing actual
Observe, its existing bootstrap attempt/record, fixed generated credential,
pinned TLS roles, durable initialized settings journal, and a root-owned fixture
driver retaining the actual Service pointer. No production credentials or hosts.
The existing command exposes neither an HTTP recovery route nor a reload signal;
a fixture driver must call existing Suspend/Resume explicitly. Do not add an
unauthenticated or generic recovery endpoint.

1. Construct the service with immutable `ReadOnly: true`. Record bootstrap file
   digests, controller/source identity and journal lineage/history. Query only
   fixed configured GET routes using the allowed mTLS leaf; settings must report
   `writes_enabled: false`. Wrong/missing roles and non-GETs must be denied with
   no upstream mutation or journal intent.
2. Obtain one valid advisory observation. Interrupt a read specifically while
   its topology check is pending, using controlled fixture instrumentation with
   an explicit entry witness. A generic HTTP cancellation is insufficient: it
   may occur before the server sees the request or during the upstream exchange,
   which has different revocation semantics. Reuse the cancelled-check unit
   test separately if no honest topology-entry witness is available.
3. Require the old session's subsequent fixed GET to fail, even with a fresh
   client context. Require the failed refresh to remove cached advice (and
   independently check expiry). Do not interpret failure as proof of a changed
   host or of physical resource release. Do not automatically reopen.
4. The fixture explicitly invokes Suspend with a bounded context. With an
   entered, controlled in-flight handler, require timeout to return failure and
   forbid proceeding to recovery. Release the handler and explicitly Suspend
   again; require drained handlers, closed old listener, cleared session and
   stopped state. Leave the stock process and its storage unchanged.
5. First attempt Resume with a cancelled context: no listener/session or policy
   change is allowed. Then explicitly Resume with a fresh bounded context and
   the same service configuration. This must perform actual Observe, existing
   bootstrap lineage validation, login and controller match; no first-run,
   credential regeneration, new journal or automatic retry policy is permitted.
6. Through the new authenticated listener, require the same fixed GET scopes,
   read-only settings flag and role/non-GET denials. Require identical controller,
   source identity and bootstrap file digests while topology stayed unchanged.
   Journal lineage/history/pending intents must be unchanged. Old sessions must
   remain revoked. Count stock calls: no first-run or resource/settings mutation;
   explicit authentication POSTs are expected and must not be mislabeled writes
   to pools or a least-privilege stock credential.
7. Require advisory recovery only after a new successful observation. Finally
   Close and join the fixture; assert no listener or background polling remains.
   Report environment identity, exact source, all assertions and any failure.

A live-context Resume failure must leave the service stopped and unavailable;
never repair that test by broadening topology, sharing the admin credential,
resetting history, or changing the production timeout. Successful recovery is a
separate hosted qualification dependency, not inferred from hermetic tests.
