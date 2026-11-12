# M2 packet-sender observation, not launch authentication

`internal/supervisorsender` is a disconnected Linux transport prerequisite. It
observes one packet from a caller-owned, already-connected AF_UNIX
SOCK_SEQPACKET endpoint. It does not import M1/audit, the resolver, admission,
GARM, Incus, gateway or application services. It creates no listener or pathname,
launches no process, signals no process and authorizes no action. Its tests use
only owned socketpairs, pipes and a fixed helper copy of the test binary.

## Implemented distinction

Connection-time `SO_PEERCRED` can continue to report a parent after its endpoint
is passed to a child. `PrepareEndpoint` enables receiver-side `SO_PASSCRED` and
`SO_PASSPIDFD` before traffic. `Observe` checks them again and requires exactly
one kernel `SCM_CREDENTIALS` and one `SCM_PIDFD` in the SAME packet. The owned
child fixture shows the actual packet sender is the child, while SO_PEERCRED
still identifies the parent. Claimed role text in the payload has no authority.

The pidfd comes from Linux's packet credential handling, not `pidfd_open` on a
later numeric PID. See official Linux v6.12
[`scm_recv_unix` / `scm_pidfd_recv`](https://github.com/torvalds/linux/blob/v6.12/include/net/scm.h).
The implementation retains the delivered close-on-exec descriptor without
exposing it or any signal/wait/launch API. `Snapshot` returns copied metadata and
bytes only while the observation retains that descriptor. Value copies share
Close state; zero/closed observations return no snapshot. No raw claimed JSON
can construct a public observation.

Maximum payload is 4096 bytes. Oversize/empty/truncated packets, ancillary rights,
missing/duplicate/unknown metadata and cancellation refuse with no observation.
Every delivered known descriptor is accounted for and closed on refusal;
rights/payload/control-truncation tests check actual descriptor counts. Receive
waiting has a soft five-second/context bound using nonblocking recvmsg/poll,
not a hard guarantee about kernel scheduling. There is no stream, connection
credential-only, pathname socket or later-PID fallback, and no automatic retry
of a consumed/ambiguous packet.

The caller must exclusively own the endpoint and its queue/options, and prepare
it before transmission. An unsuccessful option setup can leave partial receive
options set; discard that endpoint rather than treating it as qualified. The
caller-selected endpoint is NOT an enrolled broker, and this API does not prove
which party originally provisioned it. Unsupported/denied kernel facilities fail
closed. Local tests can explicitly skip unavailable socketpair/kernel support;
the hosted required step sets `GCC_SENDER_PIDFD_TESTS=1` and must run, not skip.

## Permanent limits

This is packet sender observation, NOT code/image/process-role authentication,
assignment control, job binding, a durable commit, external rollback anchor,
current connection-FD-holder authentication or permission to execute. A queued
packet can outlive its sender. Privileged senders can supply credential values
permitted by the kernel; a hostile privileged host is outside this mechanism.
Same UID, PID, endpoint access or pidfd presence is not enrollment. No comparison
of pidfd inode numbers across kernels is used as an incarnation authority.
A trusted receiver must separately bind a protected, qualified actor/generation
and a closed request policy; those facilities are absent. This observation does
not activate a supervisor, establish drain, permit deletion or release a charge.

## Concrete protected-launch boundary still required

Official Go 1.25.0
[`syscall/exec_linux.go`](https://github.com/golang/go/blob/go1.25.0/src/syscall/exec_linux.go)
provides a useful next primitive: `UseCgroupFD` selects clone3 with
`CLONE_INTO_CGROUP`; clone failure returns instead of falling back to fork and
later migration. `PidFD` can request a birth-time process handle. This source
assessment is NOT a native test or installed runtime qualification.

Plain `os.StartProcess` is NOT the sandbox. It still uses pathname exec; its
attributes do not themselves implement immutable fd-bound exec, no_new_privs,
seccomp, protected hook scratch, closed mounts/credentials/network or every
runner entry path. Go setup/runtime work before exec must be distinguished from
the first intended helper/payload instruction. A purpose-built qualified native
init/runtime, not unreviewed post-start callbacks, must establish those rules.

The next actual launch harness needs a separately approved disposable environment
with a qualified cgroup-v2 domain/delegation, isolated job/control UIDs and
mount/runtime policy, an immutable fixed test helper, held cgroup/pidfd handles
and preplanned descriptors/environment. It must test:

1. Birth is in the target native group BEFORE releasing the helper's first
   intended payload; no fork-then-move window or alternative launcher exists.
2. Missing, closed, wrong-type, undelegated or denied cgroup FD and denied clone3
   yield no payload sentinel. A failed start can still involve setup/child birth;
   it is NOT proof of no native attempt and cannot free or retry activation.
3. All accepted runtime requests are durably bound before execution. Seal rejects
   new requests while resolving every pre-seal RPC/queue, including lost replies;
   point-in-time native empty is not a held creation frontier.
4. Descendants cannot migrate outside the retained hierarchy, receive control
   credentials/IPC or enter fallback script/Node/Docker/manual/update routes.
5. Supervisor loss, boot/PID reuse and restored history cannot authorize another
   start. M1 alone lacks the independent rollback anchor and cannot satisfy this.

None of these launch/isolation tests has run here. No cgroup writes, namespace
changes, runtime installation, privilege changes or live actions are part of this
slice. Job service-consumption/no-new-assignment, native retirement/history and
accounting finalization remain separate unimplemented gates.

Focused check (requires actual kernel support):

```sh
GCC_SENDER_PIDFD_TESTS=1 go test -race -count=3 ./internal/supervisorsender
go vet ./internal/supervisorsender
```
