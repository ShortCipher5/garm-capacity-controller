#!/usr/bin/env bash
# Pinned public Garm code, disposable SQLite, no live runner infrastructure.
set -euo pipefail
commit=154638445c3949c1958b01812f69d9a1e4d82684
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
work=${1:?Usage: management-test.sh NEW-DISPOSABLE-DIRECTORY}
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
cp "$here/management-runner-test.go.txt" "$work/runner/capacity_management_test.go"
cp "$here/management-repository-test.go.txt" "$work/database/sql/capacity_management_test.go"
gofmt -w "$work/runner/capacity_management_test.go" "$work/database/sql/capacity_management_test.go"
cd "$work"
go test -mod=vendor -tags testing -race -count=1 -timeout=8m -run 'Test(Pool|Repo)TestSuite/TestCapacityManagement' ./runner
go test -mod=vendor -tags testing -race -count=1 -timeout=8m -run 'TestRepoTestSuite/TestCapacityManagement' ./database/sql
