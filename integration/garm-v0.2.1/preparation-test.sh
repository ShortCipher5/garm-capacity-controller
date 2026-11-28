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

# Run extra tests as ephemeral files. The following gateway harness requires
# the upstream tree to exactly match the previously recorded canonical patch.
(
  label_test=database/sql/stock_pool_label_qualification_test.go
  timeout_test=auth/stock_bootstrap_timeout_qualification_test.go
  for fixture in "$label_test" "$timeout_test"; do
    if [[ -e "$fixture" || -L "$fixture" ]]; then
      echo "Refusing to replace existing test fixture: $fixture" >&2
      exit 1
    fi
  done
  trap 'rm -f -- "$label_test" "$timeout_test"' EXIT

  # Check the unmodified stock SQL label matcher with a disposable repository pool.
  cp "$here/stock-pool-label-test.go.txt" "$label_test"
  go test -mod=vendor -tags testing -race -count=1 -timeout=2m \
    -run '^TestPoolsTestSuite$/^TestStockPoolRequiresEveryWorkflowLabel$' ./database/sql

  # Measure how stock applies its bootstrap timeout using only a synthetic token.
  cp "$here/stock-bootstrap-timeout-test.go.txt" "$timeout_test"
  GCC_FIXTURE_BOOTSTRAP_MINUTES=$(python3 - "$here/stock-incus-contract.toml" <<'PYTOML'
import sys
import tomllib
with open(sys.argv[1], "rb") as source:
    print(tomllib.load(source)["pool"]["runner_bootstrap_timeout"])
PYTOML
  )
  GCC_FIXTURE_BOOTSTRAP_MINUTES="$GCC_FIXTURE_BOOTSTRAP_MINUTES" \
    go test -mod=vendor -tags testing -race -count=1 -timeout=2m \
      -run '^TestStockBootstrapTimeoutUsesMinutes$' ./auth
)

go vet -mod=vendor -tags testing ./runner/common ./runner/providers/v0.1.1 \
  ./runner/pool ./workers/scaleset ./workers/provider ./util/github
