#!/usr/bin/env bash
# Read-only Ubuntu 24.04 hosted package availability discovery.
set -euo pipefail
umask 077
export LC_ALL=C
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted ]]
. /etc/os-release
[[ "$ID" == ubuntu && "$VERSION_CODENAME" == noble && "$(uname -m)" == x86_64 ]]
work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT
mkdir -m 0700 "$work/gnupg"
export GNUPGHOME="$work/gnupg"
publisher='4EFC590696CB15B87C73A3AD82CC8797C838DCFD'
base='https://pkgs.zabbly.com/incus/lts-7.0/dists/noble'
get() {
  curl --silent --show-error --fail --proto '=https' --tlsv1.2 \
    --connect-timeout 10 --max-time 30 --max-filesize "$3" \
    --output "$2" "$1"
}
get https://pkgs.zabbly.com/key.asc "$work/key.asc" 65536
primary="$(gpg --batch --with-colons --show-keys "$work/key.asc" | awk -F: '
  $1 == "pub" { pub++; waiting=1 }
  $1 == "fpr" && waiting { print $10; waiting=0 }
  END { if (pub != 1 || waiting) exit 1 }
')"
[[ "$primary" == "$publisher" ]] || { echo 'Unexpected publisher key' >&2; exit 1; }
gpg --batch --dearmor --output "$work/publisher.gpg" "$work/key.asc"
get "$base/InRelease" "$work/InRelease" 1048576
gpgv --keyring "$work/publisher.gpg" --output "$work/Release" "$work/InRelease"
# Select strongest preferred compression only if the exact entry is signed.
entry=
for candidate in main/binary-amd64/Packages.xz main/binary-amd64/Packages.gz main/binary-amd64/Packages; do
  entry="$(awk -v file="$candidate" '
    /^[^[:space:]]/ { in_hashes=($0 == "SHA256:") }
    in_hashes && NF == 3 && $3 == file { print $1, $2; count++ }
    END { if (count > 1) exit 1 }
  ' "$work/Release")"
  [[ -z "$entry" ]] || break
done
[[ -n "$entry" ]] || { echo 'No signed SHA256 entry for Noble amd64 Packages' >&2; exit 1; }
read -r digest size <<<"$entry"
[[ "$digest" =~ ^[0-9a-fA-F]{64}$ && "$size" =~ ^[1-9][0-9]{0,7}$ && "$size" -le 16777216 ]]
get "$base/$candidate" "$work/index" "$size"
[[ "$(stat -c %s "$work/index")" == "$size" ]]
printf '%s  %s\n' "$digest" "$work/index" | sha256sum --check --strict --status
(
  ulimit -f 65536
  case "$candidate" in
    *.xz) xz --memlimit-decompress=64MiB -dc "$work/index" ;;
    *.gz) gzip -dc "$work/index" ;;
    *) cat "$work/index" ;;
  esac >"$work/Packages"
)
awk -v RS='' -F '\n' '
  {
    p=v=a=""
    for (i=1; i<=NF; i++) {
      if ($i ~ /^Package: /) p=substr($i,10)
      else if ($i ~ /^Version: /) v=substr($i,10)
      else if ($i ~ /^Architecture: /) a=substr($i,15)
    }
    if (p == "incus" || p == "incus-base" || p == "incus-client") {
      if (a != "amd64" || v !~ /^[0-9][a-zA-Z0-9.+:~_-]*$/) exit 1
      print p "\t" v
      found[p]=1
    }
  }
  END { if (!found["incus"] || !found["incus-base"] || !found["incus-client"]) exit 1 }
' "$work/Packages" | sort -u >"$work/result"
cat "$work/result"
