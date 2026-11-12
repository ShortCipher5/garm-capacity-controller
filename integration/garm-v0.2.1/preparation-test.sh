#!/usr/bin/env bash
# Offline source/DB/JIT/poll qualification against disposable public source only.
# This does not contact GitHub Actions, Garm, Incus or any deployment.
set -euo pipefail
commit=154638445c3949c1958b01812f69d9a1e4d82684
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
work=${1:?Usage: preparation-test.sh NEW-DISPOSABLE-DIRECTORY}
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
if [[ -n $(gofmt -l "${files[@]}") ]]; then
  echo 'Source patch must be gofmt clean.' >&2
  exit 1
fi
git diff --check
git diff --binary > "$work.canonical.patch"
go test -mod=vendor -tags testing -race -count=1 -timeout=8m \
  ./config ./apiserver/controllers ./runner ./runner/common ./runner/providers/v0.1.1 ./runner/pool \
  ./workers/scaleset ./workers/provider ./util/github
go test -mod=vendor -tags testing -race -count=3 -timeout=8m \
  -run 'TestPoolStressTestSuite/TestPreparation|TestInstTestSuite/TestPreparation' \
  ./runner/pool ./database/sql
go vet -mod=vendor -tags testing ./runner/common ./runner/providers/v0.1.1 \
  ./runner/pool ./workers/scaleset ./workers/provider ./util/github
