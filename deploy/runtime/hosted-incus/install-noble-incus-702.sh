#!/usr/bin/env bash
# Install only the pinned daemon/client from the signed Zabbly Ubuntu 24.04 archive.
set -euo pipefail
umask 077
export LC_ALL=C
die() { printf 'native Incus 7.0.2 CI install: %s\n' "$*" >&2; exit 1; }
[[ "${GITHUB_ACTIONS:-}" == true && "${RUNNER_ENVIRONMENT:-}" == github-hosted &&
   "${RUNNER_OS:-}" == Linux && "${RUNNER_ARCH:-}" == X64 ]] || die 'requires GitHub-hosted Linux x64'
. /etc/os-release
[[ "$ID" == ubuntu && "$VERSION_CODENAME" == noble && "$(uname -m)" == x86_64 ]] ||
  die 'requires Ubuntu 24.04 amd64'
[[ ! -e /var/lib/incus && ! -L /var/lib/incus ]] || die 'preexisting daemon state'
for pkg in incus incus-base incus-client; do
  if dpkg-query -W "$pkg" >/dev/null 2>&1; then die 'preexisting Incus package state'; fi
done
[[ "$(systemctl show --value --property=LoadState incus.socket)" == not-found ]] ||
  die 'preexisting Incus socket unit'
sudo -n true
version='1:7.0.2-ubuntu24.04-202611021332'
publisher='4EFC590696CB15B87C73A3AD82CC8797C838DCFD'
work="$(mktemp -d /tmp/gcc-noble-incus.XXXXXXXX)"
cleanup() {
  local rc=$?
  trap - EXIT
  # apt-get runs as root and may create protected state under this exact directory.
  if [[ $work == /tmp/gcc-noble-incus.* && -d $work && ! -L $work ]]; then
    if ! timeout 15s sudo -n rm -rf -- "$work"; then
      printf 'Could not remove the temporary APT workspace\n' >&2
      if (( rc == 0 )); then rc=1; fi
    fi
  fi
  exit "$rc"
}
trap cleanup EXIT
chmod 755 "$work"
mkdir -m 0755 "$work/sources" "$work/lists" "$work/archives"
mkdir -m 0700 "$work/gnupg"
export GNUPGHOME="$work/gnupg"
# Standard hosted Ubuntu's signed archive is the only dependency source.
ubuntu_keyring=/usr/share/keyrings/ubuntu-archive-keyring.gpg
[[ -s "$ubuntu_keyring" ]] || die 'Ubuntu archive keyring absent'
cat >"$work/sources/ubuntu.sources" <<EOF
Types: deb
URIs: https://archive.ubuntu.com/ubuntu/
Suites: noble noble-updates
Components: main universe
Architectures: amd64
Signed-By: $ubuntu_keyring

Types: deb
URIs: https://security.ubuntu.com/ubuntu/
Suites: noble-security
Components: main universe
Architectures: amd64
Signed-By: $ubuntu_keyring
EOF
curl --silent --show-error --fail --proto '=https' --tlsv1.2 --connect-timeout 10 \
  --max-time 30 --max-filesize 65536 --output "$work/key.asc" https://pkgs.zabbly.com/key.asc
primary="$(gpg --batch --with-colons --show-keys "$work/key.asc" | awk -F: '
  $1 == "pub" { pub++; waiting=1 }
  $1 == "fpr" && waiting { print $10; waiting=0 }
  END { if (pub != 1 || waiting) exit 1 }
')"
[[ "$primary" == "$publisher" ]] || die 'unexpected Zabbly signing key'
gpg --batch --dearmor --output "$work/zabbly.gpg" "$work/key.asc"
chmod 644 "$work/zabbly.gpg"
cat >"$work/sources/zabbly.sources" <<EOF
Types: deb
URIs: https://pkgs.zabbly.com/incus/lts-7.0
Suites: noble
Components: main
Architectures: amd64
Signed-By: $work/zabbly.gpg
EOF
# Negative by default for any unrelated Zabbly package; Ubuntu dependencies retain
# their normal archive priority. These settings are scoped to these commands.
cat >"$work/preferences" <<'EOF'
Package: incus-base incus-client
Pin: origin pkgs.zabbly.com
Pin-Priority: 500

Package: *
Pin: origin pkgs.zabbly.com
Pin-Priority: -1
EOF
chmod 644 "$work/sources/"*.sources "$work/preferences"
opts=(
  -o "Dir::Etc::sourcelist=/dev/null"
  -o "Dir::Etc::sourceparts=$work/sources"
  -o "Dir::Etc::preferences=$work/preferences"
  -o "Dir::Etc::preferencesparts=-"
  -o "Dir::State::lists=$work/lists"
  -o "Dir::Cache::archives=$work/archives"
  -o "Dir::Cache::pkgcache="
  -o "Dir::Cache::srcpkgcache="
  -o "Acquire::Languages=none"
  -o "Acquire::Retries=2"
  -o "Acquire::https::Timeout=30"
  -o "Acquire::AllowInsecureRepositories=false"
  -o "Acquire::AllowDowngradeToInsecureRepositories=false"
  -o "APT::Get::AllowUnauthenticated=false"
)
timeout 240s sudo -n env DEBIAN_FRONTEND=noninteractive LC_ALL=C apt-get "${opts[@]}" \
  -o APT::Update::Error-Mode=any update -q
pkgargs=(--no-remove --no-install-recommends install "incus-base=$version" "incus-client=$version")
timeout 35s sudo -n env DEBIAN_FRONTEND=noninteractive LC_ALL=C apt-get "${opts[@]}" -s "${pkgargs[@]}" >"$work/plan"
if grep -qE '^(Remv |The following packages will be DOWNGRADED:)' "$work/plan"; then
  die 'APT proposed package removal or downgrade'
fi
for pkg in incus-base incus-client; do
  awk -v pkg="$pkg" -v ver="$version" '
    $1=="Inst" && ($2==pkg || $2==pkg ":amd64") && $3=="(" ver { found++ }
    END { exit found != 1 }
  ' "$work/plan" || die "APT did not plan exact $pkg package"
done

# The disposable daemon requires a verifiable SAN on its loopback TLS endpoint.
# Seed the cert before package maintainer scripts can start the daemon.
openssl req -x509 -newkey rsa:2048 -sha256 -nodes -days 1 \
  -subj /CN=garm-ci-incus -addext 'subjectAltName=IP:127.0.0.1' \
  -keyout "$work/server.key" -out "$work/server.crt" >/dev/null 2>&1
sudo -n install -d -m 0711 /var/lib/incus
sudo -n install -m 0600 "$work/server.key" /var/lib/incus/server.key
sudo -n install -m 0644 "$work/server.crt" /var/lib/incus/server.crt
timeout 360s sudo -n env DEBIAN_FRONTEND=noninteractive LC_ALL=C apt-get "${opts[@]}" \
  --yes "${pkgargs[@]}"
for pkg in incus-base incus-client; do
  [[ "$(dpkg-query -W -f='${db:Status-Status} ${Version}' "$pkg")" == "installed $version" ]] ||
    die "incorrect installed $pkg package"
done
sudo -n cmp "$work/server.crt" /var/lib/incus/server.crt || die 'preseed certificate changed'
timeout 35s sudo -n systemctl start incus.socket
timeout 90s sudo -n incus admin waitready
[[ "$(sudo -n incus query /1.0 | jq -er .environment.server_version)" == 7.0.2 ]] ||
  die 'unexpected Incus server API version'
printf 'Verified native Incus daemon/client: %s\n' "$version"
