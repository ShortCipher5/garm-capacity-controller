# Offline joined control-plane integration

Run `go test -race ./integration/offlinestack` from the repository root.

This suite connects real managed transport, source validation, demand planning,
immutable profile enrollment, SQLite grant issuance/redemption, original JIT
production, native coordinator, protected bootstrap codec, durable runner/job
correlation and historical root-fence records. It performs no live infrastructure
access and does not unlock any production guard.

The native owner and managed supervisor are explicitly protocol test servers.
The native fixture uses real canonical ledger claims and materializes the actual
received protected payload, but does not launch Incus, mount ZFS, execute PID1,
run a GitHub job, or establish a physical host boundary. The managed fixture
supplies scoped responses; actual root supervisor/Docker qualification remains
separate. TLS performs real server-chain/hostname checks and mutual client
certificate validation. GitHub observations use the real scoped HTTP client.

Cases cover:

- Demand produces exactly one durable grant; a repeated cycle uses that grant.
- A lost grant caller result followed by SQLite close/reopen creates no duplicate.
- CREATE, bootstrap and START responses are physically dropped after the ledger
  claim. No subsequent step is attempted after uncertainty; process restart does
  not replay any mutation or release its capacity charge.
- Authentic source identity is retained through the native incarnation binding.
- Canceling an observer blocked in a real HTTP request cancels the request and
  joins its service loop without a receipt or released charge.
- Actual GitHub in-progress discovery becomes durable exact-job retry after
  restart; completed state alone never releases capacity.
- A lost owner journal response is retried as a read after restart, without
  rereading/recreating a terminal job or inferring deletion.
- Historical unqualified root receipts remain historical: they do not trigger
  repeat owner reads and cannot authorize physical release.

A passing suite proves this offline protocol/accounting composition only. It is
not a deployment certificate or evidence that the real kernel/owner/boot chain
has been exercised in this environment.

Retirement coverage extends the same actual managed/JIT/ledger/observer path with
STOP and DELETE protocol doubles: successful completion, either POST response
lost after its durable one-use claim, unknown STOP preventing DELETE, and unknown
DELETE surviving reopen without resubmission. Every case retains the original
capacity charge; `ReleaseAuthorized` remains false. These observations do not
prove real process exit, physical storage removal, or a qualified host boundary.
The retirement fixture uses a 10-second terminal polling budget to accommodate
complete provenance validation under the race detector; production defaults and
explicit timeout safety tests are unchanged.
