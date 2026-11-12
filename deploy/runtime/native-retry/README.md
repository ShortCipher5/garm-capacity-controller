# Native transaction retry component

This lab adds exactly one test to pristine Incus 6.0.4 at `a99413afd0c742da3bba10eb4f75d639f9dbbd32`. It does not include the pending guard foundation or modify production sources.

Actual `NewTestCluster`, `Cluster.Transaction`, `query.Transaction` and `query.Retry` execute against the native cowsql test cluster. The first callback explicitly commits or rolls back through cowsql, then returns an **injected** deadline, busy or cancellation error. Six serial cases check committed identity replay refusal, confirmed rollback retry, cancellation, and an `INSERT OR IGNORE` counterexample where apparent wrapper success conceals replay. A subsequent wrapper cannot adopt the retained identity. This is not process-restart evidence.

**Not a genuine dropped transport ACK test.** Commit success is known to the fixture before it injects an error. No guard permit, physical effects, storage completion, exclusive native writer or release authority is qualified. A true wire-loss test needs separately reviewed response interception and independent durable verification.

## Isolation and evidence

The official digest-pinned Debian 13 build installs signed distribution dependencies, compiles only the DB component test binary, then executes only `TestQualificationNativeCommittedRetry` three times with a 120-second total test timeout. Execution uses UID/GID 65532 and BuildKit `RUN --network=none`. Native test-cluster library threads and a private Unix socket operate on owned temporary state, with each subcase cleaning its own cluster. No incusd process, LXC, guest, privileged flag, host mount/socket/device, credentials or live infrastructure is used. No executable or database is exported.

The script reserves a fresh output directory before network/build work, refuses existing files/directories/links, and copies only nine named evidence files after successful execution. The workflow uploads only those files on success. Evidence records exact upstream, recipe revision/hash, fixture hash, official base digest, compiler/package versions, test log and explicit boundary flags.

Run `deploy/runtime/native-retry/test.sh` only on an authorized disposable builder with Docker Buildx. Fixture is a `.go.txt` template to avoid controller-module discovery; it is copied to `internal/server/db/native_retry_qualification_test.go` in the pristine upstream archive. Hosted runtime evidence is pending until the actual exact-head job succeeds. Existing compile/guarded-caller publication holds are not lifted by this lab.
