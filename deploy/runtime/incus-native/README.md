# Disposable Incus compile and dormant native DB qualification

This public-source-only lab reconstructs pinned Incus 6.0.4 plus the reviewed
unregistered native DB candidate and offline qualification fixtures. It compiles
the actual daemon test binary WITHOUT executing it; runs native DB SQLite tests
and bounded qualification fixtures; and exports whitelisted public provenance.
It does not run a daemon, activate a schema migration, provision a guest, connect
to live infrastructure, publish an image, or authorize physical capacity release.

- Upstream: `a99413afd0c742da3bba10eb4f75d639f9dbbd32`.
- Reviewed patched source tree: `4cf349c2bbe7aa423dcf09d3768269c1ac206fe3`.
- Patch SHA256: `2706c48f48e0919ee512750dda25a8ef85e377cf9d6edaf936104934d9518975`.
- Debian 13 slim AMD64 manifest: `sha256:7378260c0a098d27ac206225db5e7d92b8c7c84ea7afccc86eeb513756bbd392`.
  Verified from official [docker-library repo-info](https://github.com/docker-library/repo-info/blob/master/repos/debian/remote/13-slim.md), dated November 5, 2026.

The patch adds dormant native database migration/helpers/tests and qualification
research; no existing upstream file is modified. Migration is not registered
and helpers have no daemon callers. The native package's actual row/device/
config/profile mapper composition and retained attempts are tested. Source
identity, native locks, complete mutation coverage, storage callbacks, cowsql
commit faults, whole-asset capacity and release remain unqualified.

The dedicated GitHub-hosted Ubuntu job is a disposable compile lab, not the
self-hosted production CI route. It has read-only repository permission, no
persisted checkout credential or secret input, and no privileged/device/bind
options. Dependencies come from the official Debian image's signed apt sources
and pinned Go modules. Apt service startup is denied inside the build. No host
packages or security settings are changed. Fork PRs do not run this job.

Exact package/compiler/base/source/recipe references and binary checksum are
retained; the executable, generated DBs and generic logs are NOT uploaded. Build
success is separate from daemon runtime and deployment readiness. Package
availability and native linkage must be learned from the actual hosted run;
local passing SQLite tests cannot substitute for it. Do not use this recipe on
credential-bearing production runners or add an automatic live deployment.

Debian's development package is `lxc-dev`, verified against the official
[Trixie package page](https://packages.debian.org/trixie/lxc-dev); `libcowsql-dev`
is also available in [Trixie](https://packages.debian.org/trixie/libcowsql-dev).
This checks package naming, not a successful build or frozen apt version set.
`lxc-dev` may pull the lxc tools as dependencies; policy-rc.d denies package
service startup and no LXC/Incus command is invoked by this build.
