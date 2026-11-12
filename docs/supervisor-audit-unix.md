# Non-executing Unix audit adapter

`internal/supervisoraudit.UnixRecorder` is an actual bounded Unix-stream protocol
adapter to the [M1 SQLite claim journal](supervisor-audit-m1.md), not a supervisor
or an authoritative broker. It has no daemon, listener/socket creation, CLI,
runtime callback, launch, activation, enrollment, drain, deletion or release path.
No existing application imports it. M2/native qualification remains absent.

The caller provides an accepted `*net.UnixConn`, the existing journal, and a
configured allowed UID. The adapter owns/closes that connection. It reads actual
Linux `SO_PEERCRED` and requires a positive PID, stream socket and matching UID
**before reading the request**. No JSON UID/PID/credential assertion is accepted.
Ancillary messages are refused; delivered SCM_RIGHTS descriptors are closed,
including the delivered portion of truncated control data. They are never used or
forwarded. Linux Go ReadMsgUnix receives descriptors with CLOEXEC.

One request consists of a four-byte big-endian nonzero byte length, at most 4096
bytes of strict JSON, then client write-half EOF. All five keys are mandatory:

```json
{"protocol":"supervisor-audit/v1","command":"record_claim","history_id":"<64 lowercase hex>","scope_digest":"<64 lowercase hex>","attempt_digest":"<64 lowercase hex>"}
```

Duplicate/escaped duplicate, unknown/case-folded, null/non-string, malformed,
unsupported-command, wrong-history, truncated, oversized, trailing and pipelined
inputs refuse without recording. The existing journal validates complete indexed
history and commits fact+audit before an acknowledgment. Separate handles and
connections retain M1's exact replay/conflict semantics. The framed response says
`outcome:recorded_history`, contains the original fact/replay and fixed M1 boundary,
and **always** includes `activation_authorized:false`, `execution_authority:none`.
Neither a new fact nor an exact replay grants permission to do anything.

Socket I/O has a five-second bound, shortened by the supplied context deadline;
cancellation closes the owned descriptor. A recorder serializes its journal
submissions with cancellation-aware waiting. Existing SQLite commit/storage
operations are not made safely cancellable or given an end-to-end completion
guarantee by this transport. A cancellation/lost reply can follow a committed
fact. Unknown commits inherit M1 poison and emit no fact/reply. Exact reconnect
can recover historical replay after reopening, never a retry-execution token.
No raw input, SQLite error or peer-supplied diagnostic is sent on rejection.

## What this intentionally does not authenticate

This is only a server-side accepted-connection history acknowledgment. There is
no production client, client-side server authentication or mutual endpoint
authentication. The acknowledgment is not an externally verified receipt and
must never be promoted into action authority.

* SO_PEERCRED identifies connect-time credentials, not the current FD holder,
  process birth, installed code, generation, per-message sender or control role.
  A real subprocess test receives a parent's connected descriptor and records a
  claim while the socket still reports the **parent's PID**. Matching UID is not
  isolation, even with a real kernel observation.
* The configured UID and scope/attempt digests are caller configuration/claims,
  not measured enrollment. Another process under that UID can submit claims or
  deny availability. An actual M2 broker needs separate protected control/job
  identities and a qualified process/native-handle/credential boundary.
* This connection-only adapter does **not** bind, inspect or qualify a pathname,
  parent, socket owner/mode, symlink resolution or endpoint uniqueness. Its caller
  must establish a protected endpoint without adopting/replacing old paths.
  A test connects through a symlink to a loose-mode socket inside a private test
  parent: the adapter records history but attests **nothing** about that path.
  Private directories/file guards are not protection against hostile same-UID or
  root, and are not all-entry-path isolation.
* M1's closed-snapshot rollback counterexample remains applicable. There is no
  independent rollback anchor, external fencing, activation uniqueness, native
  creation frontier, no-new-assignment/control frontier, no-restart, completion,
  immutable retirement or physical absence proof.

## Test environment

Socketpair, ancillary rejection/FD closure, strict protocol, UID denial-before-read,
concurrent handles, cancellation, actual own-child descriptor delegation, lost reply
and committed-but-unknown recovery run without host privileges. Child processes
are only this package's Go-test binary with explicit minimal environment and owned
fixture descriptors. No shells, services, namespaces, containers or VMs run.

This execution environment refuses new AF_UNIX socket creation with EPERM; it is
not bypassed. Named-endpoint tests require `GCC_AUDIT_UNIX_BIND_TESTS=1` in an
approved disposable environment. The existing ordinary hosted Go CI matrix runs
an explicit required step with that flag, including a real independently connecting
child credential check and the pathname counterexample. Without the flag those
two cases visibly skip: local socketpair success is not named-endpoint or host
isolation qualification. No sudo, install, daemon/host socket, bind mount, policy
change or privileged runtime is required or permitted by these tests.
