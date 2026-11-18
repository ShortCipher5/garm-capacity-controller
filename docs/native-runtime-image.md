# Pinned native runtime image packager

`capacity-native-image` packages a supplied, root-owned runtime filesystem into a
new raw ext4 image. It does not download or install software, mount the image,
configure a host, launch Incus, or activate the controller.

The JSON manifest names every regular file by SHA-256 and final mode (0444 or
0555), and every symlink by exact target. It binds architecture, epoch, size,
Incus 6.0.4 and ZFS 2.2.6. Required ELF entrypoints are the native stage, quota
reader, Incus daemon and ZFS command. All ELF files must match the architecture;
interpreters and direct `DT_NEEDED` libraries must resolve into the declared
inventory. RPATH/RUNPATH, missing dependencies, links escaping the root, special
files, hard links, file capabilities/xattrs, set-id files, extra files and nested
mounts are refused. Source and destination ancestors must be root-owned and not
writable by other users.

The fresh materialized filesystem uses fixed ordering, modes and timestamps.
`/dev`, `/proc`, `/sys`, `/run`, `/tmp` and the delegated runtime mountpoint are
empty placeholders, never prepopulated with host content. `fstab` and machine-id
inputs are refused. No writable host directory is bound by this packager.

The root-owned mke2fs binary and a separate invocation of the image executable
are SHA-pinned. The helper strips ambient descriptors before the fixed mkfs
invocation, retains only its fixed input/output descriptors, and uses an empty
mke2fs configuration with explicit filesystem features, UUID/hash seed, epoch,
inode count and block size. The parent builds twice and compares every byte via
SHA-256. A mismatch or failure leaves the exclusive attempt directory incomplete
and never creates a completion receipt. Output always reports `BootQualified:
false`. No existing directory or image is adopted/overwritten.

Upstream supports reproducible filesystem construction with fixed epoch:
- https://github.com/tytso/e2fsprogs/blob/master/doc/RelNotes/v1.47.1.txt
- https://github.com/systemd/systemd/blob/v257/src/shared/mkfs-util.c

## Inputs still needed for a qualified artifact

This repository does not supply the pinned Incus/ZFS distribution binaries or a
qualified full runtime filesystem. The operator must supply those exact artifacts
and their complete reviewed inventory, including helpers/plugins/data files that
are loaded dynamically rather than represented in ELF `DT_NEEDED`. Version labels
in the manifest are requirements, not independent proof of the binaries' version.
SHA pins identify supplied bytes; they do not establish vendor provenance.

The build's repeated-output check is a reproducibility gate, not a boot test.
Actual nspawn startup, Incus helper discovery, ZFS ioctl/user-namespace behavior,
network configuration and runner isolation still require qualification. The
active native owner remains refused until its entire boundary is implemented and
reviewed. Successful packaging never grants runner capacity or physical release.

## Patched LXC contract (not qualification)

Manifest v3 requires exact LXC version `7.0.1` and an explicitly inventoried
`liblxc.so.1` regular ELF at a supported loader path. Its bytes are hash-bound
by the complete image manifest. Native discovery independently requires actual
Incus `environment.driver == lxc` and `driver_version == 7.0.1`; additional
VM drivers, missing versions, suffixes and older libraries are refused.
Neither a manifest version string nor daemon readback proves upstream provenance.

The upstream advisory GHSA-4xwm-pr6h-55r8 identifies versions before 7.0.1 as
affected: https://github.com/lxc/lxc/security/advisories/GHSA-4xwm-pr6h-55r8.
The official v7.0.1 source/release URLs returned 404 during this review, so
actual patched artifact provenance, Incus 6.0.4 ABI compatibility and the new
LXC cgroup-placement semantics are NOT qualified. Existing v6.0.4 parser
research must not be represented as validation of v7.0.1. Activation remains
refused. No locally patched fork or unverified distro suffix is accepted.

Incus's actual source emits driver names and versions from supported driver
statuses: https://github.com/lxc/incus/blob/v6.0.4/cmd/incusd/api_1.0.go.

Maintained distro follow-up: Debian Sources publishes `lxc 1:7.0.0-3`
with patch `0005-cherry-pick-GHSA-4xwm-pr6h-55r8.patch`, containing upstream
commits `06f626091cb9f1d717cb9d63b7bc2ffdbf0c3cdf` and
`19ad80ed8e6288904caaee5b7bd3c485d51a2291` (both command-socket handlers).
Source: https://sources.debian.org/patches/lxc/1:7.0.0-3/0005-cherry-pick-GHSA-4xwm-pr6h-55r8.patch/ .
This is a candidate maintained package, not yet an accepted runtime profile:
its signed binary package/hash-to-extracted-library provenance, actual reported
library version and Incus/cgroup compatibility still require verification.
A plain `7.0.0` daemon string cannot distinguish patched from vulnerable builds,
so the current gate deliberately rejects it. Do not relax that gate on a
package name, caller-supplied checksum or advisory URL alone.
