# Real CreateInternal component baseline

This separate disposable lab executes pinned Incus6.0.4's actual
`instance.CreateInternal` with its actual native test-cluster helper. It is NOT
the compile-only native experiment, and does not include its dormant patch.
Only the supplied same-package test is overlaid onto pristine upstream commit
`a99413afd0c742da3bba10eb4f75d639f9dbbd32`.

The test is serial and temporarily replaces the existing `instance.Create`
factory with a sentinel callback, restoring it on exit. That callback checks a
committed native instance/device row using a separate native transaction and
checks the real create operation lock remains held. It returns a sentinel
error BEFORE native driver/device/log/storage execution. The real caller's
error cleanup must remove its ordinary row and release its operation lock.
Invalid input must not reach the sentinel factory. This baseline does not
insert guard history or prove retained-accounting integration.

## Runtime boundary

- Official pinned Debian13 build with signed distribution dependencies. Package
  service startup denied. No install scripts or unrecognized binaries.
- Compile only the `internal/server/instance` test binary with race support.
  Never compile/run `incusd` as an executable daemon in this workflow.
- Execute only the exact new test, three times, as UID/GID65532, network disabled,
  with a120-second test timeout. No privileged flags/devices/capabilities/host
  sockets/binds. No container runner or LXC guest is started.
- `db.NewTestCluster` invokes native cowsql library startup IN the test process.
  Its helper creates a private Unix socket address and owned temporary directory,
  starts native library threads, opens the actual cluster DB, and closes/removes
  owned state in deferred cleanup. A failing component test is not permission
  to change isolation, host privileges or production configuration.
- Docker build sandbox contains all runtime state. Successful export includes
  nine enumerated evidence files only, not binary/database/private state.
  Existing output directories/files/symlinks are refused before network/build.
- Native startup and real caller behavior are UNVERIFIED until this separate
  hosted test succeeds. A passing test is component coverage only, not a native
  fault/unknown-commit test, guarded writer, physical completion or release proof.

The next integration would require a separately reviewed real guarded caller
and immutable accounting propagation. This test deliberately does not pretend
that stock row cleanup proves physical rollback, or that a fake driver tests
real driver/storage/device side effects.
