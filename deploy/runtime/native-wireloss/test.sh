#!/usr/bin/env bash
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
lab=deploy/runtime/native-wireloss
parent=qualification-results
if [[ -L "$parent" || ( -e "$parent" && ! -d "$parent" ) ]]; then exit 1; fi
mkdir -p "$parent"
out="$parent/native-wireloss"
mkdir "$out" # Exclusive reservation; never adopt stale files or links.
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
revision=a99413afd0c742da3bba10eb4f75d639f9dbbd32
git init -q "$work/upstream"
git -C "$work/upstream" fetch -q --depth=1 https://github.com/lxc/incus.git "$revision"
test "$(git -C "$work/upstream" rev-parse FETCH_HEAD)" = "$revision"
mkdir "$work/source" "$work/evidence"
git -C "$work/upstream" archive FETCH_HEAD | tar -x -C "$work/source"
for name in wireloss_qualification_conn_test.go wireloss_qualification_test.go wireloss_parser_qualification_test.go; do
 cp "$lab/$name.txt" "$work/source/internal/server/db/$name"
done
cp "$lab/driver-source.sha256" "$work/evidence/driver-source.sha256"
printf '%s\n' "$revision" > "$work/evidence/upstream.txt"
git rev-parse HEAD > "$work/evidence/recipe-revision.txt"
sha256sum "$lab/"*.go.txt > "$work/evidence/fixture.sha256"
sha256sum "$lab/Dockerfile" > "$work/evidence/recipe.sha256"
printf '%s\n' 'docker.io/library/debian@sha256:7378260c0a098d27ac206225db5e7d92b8c7c84ea7afccc86eeb513756bbd392' > "$work/evidence/base-image.txt"
cp "$lab/Dockerfile" "$work/Dockerfile"
docker buildx build --platform linux/amd64 --target evidence --output "type=local,dest=$work/output" "$work"
for name in upstream.txt recipe-revision.txt fixture.sha256 recipe.sha256 base-image.txt packages.tsv go-version.txt test.txt result.txt driver-source.sha256; do
 test -f "$work/output/$name" && test ! -L "$work/output/$name"
 cp "$work/output/$name" "$out/$name"
done
