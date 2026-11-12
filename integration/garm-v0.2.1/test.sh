#!/usr/bin/env bash
# Tests only pinned public source against disposable local fixtures.
set -euo pipefail
commit=154638445c3949c1958b01812f69d9a1e4d82684
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
work=${1:?Usage: test.sh NEW-DISPOSABLE-DIRECTORY}
[[ "$work" = /* ]] || work="$PWD/$work"
if [[ -e "$work" ]]; then
  echo 'Refusing to modify an existing source directory.' >&2
  exit 1
fi
git init --quiet "$work"
git -C "$work" remote add origin https://github.com/cloudbase/garm.git
git -C "$work" fetch --quiet --depth=1 origin "$commit"
git -C "$work" checkout --quiet --detach FETCH_HEAD
[[ $(git -C "$work" rev-parse HEAD) == "$commit" ]]
git -C "$work" apply --check "$here/admission-preflight.patch"
git -C "$work" apply "$here/admission-preflight.patch"
git -C "$work" add -N -- .
cd "$work"
mapfile -t files < <(git diff --name-only -- '*.go')
gofmt -w "${files[@]}"
# Retain a canonical patch so the repository patch can be checked/reformatted
# using the same real toolchain that compiled the integration tests.
git diff --binary > "$work.canonical.patch"
go test -mod=vendor -tags testing -race -count=1 -timeout=8m \
  ./runner/common ./runner/providers/v0.1.1 ./runner/pool ./workers/scaleset

# Qualify the actual pinned external provider against our compiled executable and
# actual gateway service, using only disposable local mTLS Garm/Incus fixtures.
cd "$here/../.."
GCC_PINNED_GARM_WORKTREE="$work" go test -race -count=1 -timeout=8m \
  -run '^TestPinnedGarmCompiledProviderWire$' ./cmd/capacity-gateway
