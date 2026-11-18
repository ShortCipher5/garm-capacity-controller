# Patched stock LXC artifact provenance

This is a bounded artifact/readback contract, not a deployment approval.
No GARM or Incus fork is introduced. Production guards remain closed.

## Verified Debian artifact (2026-11-18)

The Debian archive's `unstable/InRelease` was downloaded over HTTPS and its
signature verified with the installed Debian archive keyring (`gpg --verify`,
exit 0). Valid archive signer primary fingerprints included
`B8B80B5B623EAB6AD8775C45B7C5D7D6350947F8` and
`04B54C3CDCA79751B16BC6B5225629DF75B188BD`.
The signed SHA256 entry for `main/binary-amd64/Packages.xz` was
`9ae3c05042f4e77683fa4b4294e6a34207ef22fde7ad8f6e4f79d26671ab8f31`
(size 10880304); downloaded bytes matched.
The package record identifies `liblxc1t64`, source `lxc`, version
`1:7.0.0-3`, architecture `amd64`, archive path
`pool/main/l/lxc/liblxc1t64_7.0.0-3_amd64.deb`, size 408004 and SHA256
`f38f1ab02532199d5083b6f9d0cb20ce651ffc7d55606227fb469204bebb27de`.
Downloaded package bytes matched. The package was extracted, not installed or
executed. Extracted `usr/lib/x86_64-linux-gnu/liblxc.so.1.9.0` is 1194240 bytes,
SHA256 `d6f72b1de7484cc42fa5c84e5b0429f2f544ebd0e38367b08ee5cffbd482f5c2`.
Its SONAME is `liblxc.so.1`; the package's SONAME symlink targets this file.

Official sources:
- https://deb.debian.org/debian/dists/unstable/InRelease
- https://deb.debian.org/debian/pool/main/l/lxc/
- https://sources.debian.org/patches/lxc/1:7.0.0-3/0005-cherry-pick-GHSA-4xwm-pr6h-55r8.patch/

The vendor patch includes upstream commits
`06f626091cb9f1d717cb9d63b7bc2ffdbf0c3cdf` and
`19ad80ed8e6288904caaee5b7bd3c485d51a2291`. It checks both affected command
handlers. The Debian security source-package tracker marks this package fixed.
The upstream advisory advertises 7.0.1, but that release's official tag/source
URLs were unavailable during review. A plain `7.0.0` string is not provenance.

## Enforcement in code

The manifest's Debian profile accepts only these independently verified bytes,
architecture, package version and exact real library path, plus the expected
SONAME symlink. No caller-supplied checksum can replace this artifact pin.
The ordinary image inventory and ELF dependency validation still apply.

Before accepting a capsule's Incus `driver_version=7.0.0`, the actual retained
live daemon is checked: its root filesystem library is opened beneath its root
with no symlink traversal, regular/read-only/size/hash checked, and matched to
an executable private mapping by exact device/inode and path. Other liblxc
mappings and deleted paths are rejected. Full capsule identity checks bracket
the observation. The API still requires a container-only `lxc` driver.
Non-capsule paths retain the stricter 7.0.1 requirement, not a blanket downgrade.

## Unqualified requirements

The Debian binary requires GLIBC_2.43 (confirmed in actual ELF version-needed
entries), plus dbus, seccomp, SELinux, AppArmor, cap and gcc runtime libraries.
An arbitrary older image is not ABI-compatible merely because files are present.
No real Incus 6.0.4 + this vendor library image has been booted or qualified.
Source-level LXC cgroup-path semantics and actual namespace placement still
require review against this patched source, followed by supported-host tests.
Mapped-file provenance does not prove all loaded dependencies, hostile-native
confinement, complete physical release, or absence of other vulnerabilities.

Source-only follow-up: the official Debian pool's `lxc_7.0.0.orig.tar.gz`
contains all five fixed cgroup configuration keys in `src/lxc/confile.c`
(lines 202–207); `container.inner` still requires one directory component.
`src/lxc/cgroups/cgfsng.c` retains `relative=false` PID1 current-cgroup discovery
and init.scope pruning (3080–3094, 3259–3285). This supports the existing fixed
v5 compiler at the parser/path-construction level. It does not establish actual
Incus/liblxc ABI compatibility or our constructed mount/cgroup topology. The
source tarball was inspected read-only, not executed; its separate signed source
index chain has not yet been verified in this pass.

The offline image builder now also checks GNU version-needed entries from each
hash-verified ELF against version definitions in every inventoried loader
candidate for the named dependency. Missing GLIBC_2.43 or an ambiguous older
libc candidate is refused before image construction. This is declared-version
closure, not a substitute for actual symbol binding, execution or ABI tests.
