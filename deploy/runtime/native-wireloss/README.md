# Owned native commit-response suppression

This separately scoped lab overlays only three test files on pristine Incus 6.0.4 a99413afd0c742da3bba10eb4f75d639f9dbbd32. No held guard foundations or production changes are included.

A framing-aware test connection recognizes only canonical protocol-v1 ExecSQL-v0 COMMIT, forwards it unchanged, drains the successful server Result, and verifies the durable original value over a separate unwrapped connection BEFORE returning zero response bytes and EOF to the actual driver. Three serial native cases cover direct Commit error, strict identity replay refusal in the actual transaction wrapper, and the ignored-insert counterexample. Exact one targeted commit/drop, preserved original row, callback counts and retry reconnection are asserted. Parser tests cover fragmentation, unchanged forwarding, zero exposed bytes, nonmatching SQL/opcode/schema and malformed/failed/truncated frames.

Successful hosted execution would demonstrate controlled successful-response suppression at the client transport boundary. It would NOT qualify arbitrary packet loss, quorum failure, process restart, guarded caller, storage completion, physical effects or release authority. This bounded test parser is not an authorization filter: it observes forwarded requests, not a production proxy.

## Isolation and evidence

Official digest-pinned Debian13, signed distro dependencies and no service startup; only DB test binary compilation. Runtime is nonroot UID/GID65532 with BuildKit network-none, exact two-test selector count3 and total120s timeout. Native library test server uses owned temporary state/private Unix sockets; each case closes both DB connections and server. No Incus daemon, LXC, host devices/mounts/sockets, privilege, credentials or live infrastructure.

The pristine module is verified and six pinned go-cowsql source hashes checked before compilation. Fixture templates avoid controller package discovery. Fresh exclusive output refuses stale entries before fetch/build. Success-only ten-file whitelist records source/recipe/fixture/driver pins, compiler/packages, test results and boundary flags. No raw frames, databases, executable or native-address dump is exported. Native runtime outcome remains pending until the exact-head hosted job passes; local parser-only results are not native evidence.

Run `deploy/runtime/native-wireloss/test.sh` only on an authorized disposable Buildx builder. This lab does not resolve or retry other held publication/merge actions.
