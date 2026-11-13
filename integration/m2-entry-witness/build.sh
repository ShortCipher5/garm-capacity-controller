#!/bin/sh
# Build/inspect only. This script NEVER executes the resulting witness.
set -eu
PATH=/usr/bin:/bin
export PATH
umask 077
if [ "$#" -ne 1 ]; then echo 'usage: build.sh NEW_ABSOLUTE_PRIVATE_OUTPUT_DIR' >&2; exit 2; fi
out=$1
case "$out" in /*) ;; *) echo 'output must be absolute' >&2; exit 2;; esac
if [ -e "$out" ] || [ -L "$out" ]; then echo 'output must be entirely new' >&2; exit 2; fi
src=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
mkdir -- "$out"
mkdir -- "$out/tmp"
env -i PATH=/usr/bin:/bin LC_ALL=C TMPDIR="$out/tmp" /usr/bin/gcc \
  -std=c11 -O2 -Wall -Wextra -Werror -ffreestanding -fno-builtin \
  -fno-stack-protector -fno-pie -fno-ident -fno-unwind-tables \
  -fno-asynchronous-unwind-tables -nostdlib -static -no-pie \
  -Wl,--build-id=none -Wl,--fatal-warnings -Wl,-z,noexecstack -Wl,-e,_start \
  "$src/entry.S" "$src/witness.c" -o "$out/entry-witness"
chmod 0500 "$out/entry-witness"
/usr/bin/readelf -W -h -l -s "$out/entry-witness" > "$out/elf.txt"
{
 printf 'format=garm-m2-entry-build/v1\nqualification=none\nexecution_authority=none\n'
 printf 'artifact_sha256='; /usr/bin/sha256sum "$out/entry-witness" | cut -d' ' -f1
 printf 'entry_source_sha256='; /usr/bin/sha256sum "$src/entry.S" | cut -d' ' -f1
 printf 'witness_source_sha256='; /usr/bin/sha256sum "$src/witness.c" | cut -d' ' -f1
 printf 'compiler='; env -i PATH=/usr/bin:/bin LC_ALL=C /usr/bin/gcc --version | head -n1
} > "$out/manifest.txt"
