#!/usr/bin/env bash
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
# Reserve a fresh output leaf before build. Never adopt/remove existing output,
# including dangling symlinks; reject non-directory/symlink parent too.
if [[ -L qualification-results || ( -e qualification-results && ! -d qualification-results ) ]]; then
  echo 'Unsafe existing qualification-results parent' >&2; exit 1
fi
mkdir -p qualification-results
mkdir qualification-results/incus-native
upstream=a99413afd0c742da3bba10eb4f75d639f9dbbd32
expected_tree=4cf349c2bbe7aa423dcf09d3768269c1ac206fe3
expected_patch=2706c48f48e0919ee512750dda25a8ef85e377cf9d6edaf936104934d9518975
base=docker.io/library/debian@sha256:7378260c0a098d27ac206225db5e7d92b8c7c84ea7afccc86eeb513756bbd392
printf '%s  %s\n' "$expected_patch" "$here/reviewed-native.patch" | sha256sum -c -
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/repo" "$work/context/source" "$work/context/evidence"
git -C "$work/repo" init -q
git -C "$work/repo" remote add origin https://github.com/lxc/incus.git
git -C "$work/repo" fetch --depth=1 origin "$upstream"
git -C "$work/repo" checkout --detach FETCH_HEAD
[[ $(git -C "$work/repo" rev-parse HEAD) == "$upstream" ]]
git -C "$work/repo" apply --check "$here/reviewed-native.patch"
git -C "$work/repo" apply --index "$here/reviewed-native.patch"
actual_tree=$(git -C "$work/repo" write-tree)
[[ "$actual_tree" == "$expected_tree" ]]
git -C "$work/repo" archive "$actual_tree" | tar -x -C "$work/context/source"
cp "$here/Dockerfile" "$work/context/Dockerfile"
printf '%s\n' "$upstream" > "$work/context/evidence/upstream-revision.txt"
printf '%s\n' "$actual_tree" > "$work/context/evidence/patched-tree.txt"
printf '%s\n' "$expected_patch" > "$work/context/evidence/patch-sha256.txt"
printf '%s\n' "$base" > "$work/context/evidence/base-image.txt"
git -C "$here" rev-parse HEAD > "$work/context/evidence/recipe-revision.txt"
sha256sum "$here/Dockerfile" | cut -d' ' -f1 > "$work/context/evidence/dockerfile-sha256.txt"
# BuildKit exports files without starting any container from the final image.
# No daemon/socket/devices, credentials, mounts, privileged mode, or image push.
docker buildx build --no-cache --platform linux/amd64 --target evidence \
  --output "type=local,dest=$work/output" "$work/context"
# Whitelist public status/provenance only; no binary, database or generic logs.
for name in upstream-revision.txt patched-tree.txt patch-sha256.txt base-image.txt recipe-revision.txt dockerfile-sha256.txt native-package-versions.tsv go-version.txt go-build-env.txt incusd-compile-only.sha256 result.txt; do
  test -f "$work/output/$name"
  cp "$work/output/$name" "qualification-results/incus-native/$name"
done
cat qualification-results/incus-native/result.txt
