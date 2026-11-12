# Actual source-to-gateway preparation qualification

Status: executable offline integration against pinned public source and disposable
local services. **No live deployment, native guest execution, GitHub authentication,
physical-capacity or external-writer qualification.** Source API enrollment is a
reviewed extension, not an existing stock Garm capability.

Run the mandatory fresh-source `test.sh NEW-DISPOSABLE-DIRECTORY`. For a previously
prepared canonical disposable worktree, the focused command is:

```sh
GCC_PINNED_GARM_WORKTREE=/absolute/canonical/disposable/source \
  go test -race -count=1 -timeout=8m \
  -run '^TestPinnedGarm(CompiledProviderWire|ActualSourcePreparationWire)$' \
  ./cmd/capacity-gateway
```

The full source test checks exact public source HEAD, exact canonical patch bytes
and absence of extra untracked files before injecting two no-overwrite temporary
fixtures. A test-only build tag exposes a factory for constructing the existing
normal-pool manager with controlled JIT/JWT responses. It changes no production
method and is not present in the deployable patch. Fixtures are removed afterward.
This does NOT qualify Garm daemon startup, all background loops or real issuers.

## Executed production paths

- Actual public Garm HTTP pool controller, public Runner API and SQLite enroll a
  normal pool with explicit zero template capability and SQL NULL. No direct SQL
  pool enrollment or private flag injection substitutes for the API.
- Actual `AddRunner`, strict original-JIT metadata validation, external provider
  commands, pre-SQL receipt ordering, SQL publication and pending poll/create
  helper execute. The JIT response and callback JWT are disposable test values;
  their real-service authenticity is not being asserted.
- Actual Garm HTTP inventory/pool controllers read that SAME SQLite state through
  a disposable mTLS/Bearer listener. A fixture admin context replaces Garm's user
  authentication middleware; no full user-login qualification is claimed.
- The real compiled `capacity-provider` executable uses separate source and
  ordinary provider TLS leaves. Actual runtime gateway startup, route-scoped
  issuer authorization, immutable receipt/plan/binding storage, authenticated
  Garm reads, reconciliation and prepared one-shot dispatch execute.
- Native requests go only to a disposable mTLS HTTP fixture, never Incus, a guest,
  an image or hardware. Explicit limits, correlation, submission-before-wait,
  create evidence and native-request counts are inspected independently.

The lost-Record-ACK case uses an explicitly test-only authenticated relay. It
verifies the incoming client leaf and forwards using the SAME leaf identity to
the real gateway handler, reads its successful response after commit, then closes
the source connection without delivering any response bytes. It is a fault
instrument, not a production proxy or alternative authentication authority.

## Full-source cases

1. **Confirmed.** Hold `AddRunner` AFTER real SQL commit and BEFORE it receives its
   store acknowledgement. A fresh complete gateway collection sees the actual
   positive-agent pending row, verifies its original receipt and becomes ready
   with zero physical inventory while retaining the full reservation charge.
   The independent source poll then creates exactly once.
2. **Lost SQL acknowledgement.** The same held committed row can be consumed by
   polling before `AddRunner` returns its simulated lost-ACK error. Its original
   JIT registration and ledger charge remain held; there is no second JIT fetch,
   cleanup, cancellation or duplicate native create.
3. **Wrong source role.** Use the ordinary provider leaf for RecordPreparation.
   Source publication is refused: zero Garm rows, zero native requests, no receipt
   or dispatch. The original reservation and registration remain conservative.
4. **Known generation drift.** Actual pool generation differs from the configured
   route. Admission returns defer75 BEFORE reservation or JIT: zero reservation,
   JIT calls, Garm rows and native writes. This is a read-time check, NOT remote CAS.

5. **Committed then lost Record ACK.** The authenticated relay confirms the REAL
   gateway's receipt/audit commit and drops its response. Actual AddRunner and
   compiled wrapper retain the registration and reservation; actual Garm SQL has
   zero rows, native requests remain zero, no cancellation/dispatch occurs, and
   the immutable original receipt remains stored. Exactly one upstream Record
   succeeded and one ACK was dropped: no automatic retry or blind release.

In both successful source cases, the original receipt and submitted dispatch are
already durable before native wait. Identity, source leaf, original row UUID,
positive agent, pool, host-binding sequence and preparation audit sequence remain
correlated. Private JIT/native metadata and bootstrap material are not journaled.

The separate narrower compiled-provider test preserves its original confirmed and
unknown-native-wait cases, exact create count, confirmed replay, cancellation,
backpressure, durable correlation and evidence assertions. It now adds exact
source route metadata, separate SourceTLS, record replay/conflict, actual API-row
lookup, missing-receipt Create refusal and source/provider role separation. Its
Garm row is explicitly a fixture; it is NOT a replacement for the full source
SQL/AddRunner/poll cases above.

## Remaining gates

These tests do not provide an external writer epoch, authoritative physical-asset
registry, native common-parent hard limits, foreign-project completeness, guest
isolation, image/profile qualification, actual GitHub job fencing, destructive
retirement, raw same-lineage rollback recovery or archive activation. Unknown
remote work stays charged. A successful test run does not authorize deployment,
real canaries or any live mutation.
