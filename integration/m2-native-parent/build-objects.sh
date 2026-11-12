#!/bin/sh
# COMPILE/INSPECT ONLY. Never link or execute a native parent on this host.
set -eu
PATH=/usr/bin:/bin
export PATH
umask 077
if [ "$#" -ne 1 ]; then echo 'usage: build-objects.sh NEW_ABSOLUTE_PRIVATE_DIR' >&2; exit 2; fi
out=$1
case "$out" in /*) ;; *) echo 'absolute output required' >&2; exit 2;; esac
if [ -e "$out" ] || [ -L "$out" ]; then echo 'entirely new output required' >&2; exit 2; fi
src=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
mkdir -- "$out"
mkdir -- "$out/tmp"
/bin/sh "$src/../m2-entry-witness/build.sh" "$out/witness"
env -i PATH=/usr/bin:/bin LC_ALL=C TMPDIR="$out/tmp" /usr/bin/gcc \
 -std=c11 -O2 -Wall -Wextra -Werror -ffreestanding -fno-builtin \
 -fno-stack-protector -fno-pie -fno-ident -fno-unwind-tables \
 -fno-asynchronous-unwind-tables -c "$src/parent.c" -o "$out/native-parent.o"
(
 cd -- "$out"
 /usr/bin/objcopy -I binary -O elf64-x86-64 -B i386:x86-64 \
  --rename-section .data=.rodata,alloc,load,readonly,data,contents \
  witness/entry-witness embedded-witness.o
)
chmod 0400 "$out/native-parent.o" "$out/embedded-witness.o"
/usr/bin/readelf -W -h -s "$out/native-parent.o" > "$out/parent-elf.txt"
{
 printf 'format=garm-m2-native-parent-objects/v1\nparent_linked=false\nparent_executed=false\nqualification=none\nexecution_authority=none\n'
 for item in native-parent.o embedded-witness.o witness/entry-witness; do
  printf '%s_sha256=' "$item"; /usr/bin/sha256sum "$out/$item" | cut -d' ' -f1
 done
 printf 'parent_source_sha256='; /usr/bin/sha256sum "$src/parent.c" | cut -d' ' -f1
 printf 'recipe_source_sha256='; /usr/bin/sha256sum "$src/build-objects.sh" | cut -d' ' -f1
} > "$out/manifest.txt"
