#!/usr/bin/env bash
# Actual pinned runner methods, mocked network/worker boundaries, NO registration.
# Uses an ALREADY INSTALLED .NET 8 SDK. Never runs upstream dev.sh/installers.
set -euo pipefail
umask 077
pin=d8b82f44a9ec0258904d5869ae683c31d5aac299
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
verify_source() {
  [[ $(git -C "$1" rev-parse HEAD) == "$pin" ]]
  (cd "$1" && sha256sum --check "$here/source.sha256")
  [[ $(cat "$1/src/runnerversion") == '2.323.0' ]]
}
if [[ ${1:-} == '--check-source' && $# == 2 ]]; then
  verify_source "$2"
  echo 'Exact source hashes verified. Compiled .NET lifecycle tests NOT RUN.'
  exit 0
fi
[[ $# == 1 ]] || { echo 'Usage: test.sh NEW-ABSOLUTE-DISPOSABLE-DIRECTORY | --check-source EXISTING-SOURCE' >&2; exit 2; }
work=${1%/}
[[ "$work" == /* ]] || { echo 'Requires NEW absolute source and isolated output paths.' >&2; exit 2; }
# Validate EVERY output before SDK execution, mkdir, fetch, writes or upload
# acknowledgment. -L rejects dangling symlinks as well as links to existing data.
for path in "$work" "$work.results" "$work.home" "$work.tmp" "$work.packages"; do
  [[ ! -e "$path" && ! -L "$path" ]] || { echo 'Requires NEW absolute source and isolated output paths.' >&2; exit 2; }
done
command -v dotnet >/dev/null || { echo 'Already installed .NET 8 SDK required; no installation attempted.' >&2; exit 2; }
# No -p: an output created after the preflight (including a symlink) must fail.
# Preserve any partial reservation on error; never reuse, remove or overwrite it.
mkdir -- "$work" "$work.results" "$work.home" "$work.tmp" "$work.packages"
# Artifact upload is allowed only for outputs this invocation actually reserved.
# This variable is NOT passed through clean_dotnet to the tested runner code.
if [[ -n ${GITHUB_OUTPUT:-} ]]; then
  printf 'results_created=true\n' >> "$GITHUB_OUTPUT"
fi
# No inherited runner input/JIT configuration, GitHub tokens, proxy credentials,
# real home/configuration or ambient service secrets reach the test process.
clean_dotnet() {
  env -i PATH="$PATH" HOME="$work.home" TMPDIR="$work.tmp" DOTNET_CLI_HOME="$work.home" \
    NUGET_PACKAGES="$work.packages" DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1 \
    DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE=true \
    dotnet "$@"
}
clean_dotnet --list-sdks | grep -Eq '^8\.0\.4[0-9][0-9] ' || { echo 'Preinstalled compatible .NET 8.0.4xx SDK unavailable; no installation attempted.' >&2; exit 2; }
git init --quiet "$work"
git -C "$work" remote add origin https://github.com/actions/runner.git
git -C "$work" fetch --quiet --depth=1 origin "$pin"
git -C "$work" checkout --quiet --detach FETCH_HEAD
verify_source "$work"
target="$work/src/Test/L0/Listener/RetirementSourceContracts.cs"
[[ ! -e "$target" && ! -L "$target" ]]
cp "$here/RetirementSourceContracts.cs" "$target"
mkdir -- "$work/src/Test/TestLogs"
cd "$work/src"
selected=$(clean_dotnet --version)
[[ "$selected" =~ ^8\.0\.4[0-9][0-9]$ ]] || { echo 'Upstream global.json did not select preinstalled .NET 8.0.4xx.' >&2; exit 2; }
{
  printf 'official_source_commit=%s\nselected_preinstalled_sdk=%s\n' "$pin" "$selected"
  clean_dotnet --info
} > "$work.results/toolchain.txt"
# Generate only version metadata, not upstream Build/Layout (which downloads
# SDK/externals). Preserve all upstream audit/analyzer/warning policies.
clean_dotnet msbuild dir.proj -t:GenerateConstant -p:PackageRuntime=linux-x64 -p:RunnerVersion=2.323.0
# NuGet dependencies are the upstream project's pinned public registry packages.
# There are no real service tokens, credentials, jobs, listeners or host writes.
clean_dotnet test Test/Test.csproj -c Release -p:PackageRuntime=linux-x64 -p:RuntimeIdentifier=linux-x64 -p:RuntimeIdentifiers=linux-x64 \
  --filter 'FullyQualifiedName~RetirementSourceContracts' \
  --logger 'trx;LogFileName=retirement-source-contracts.trx' --results-directory "$work.results"
verify_source "$work"
cmp "$target" "$here/RetirementSourceContracts.cs"
python3 - "$work.results/retirement-source-contracts.trx" <<'PY'
import sys
import xml.etree.ElementTree as ET
root = ET.parse(sys.argv[1]).getroot()
ns = {'t': 'http://microsoft.com/schemas/VisualStudio/TeamTest/2010'}
expected = {
 'ShutdownCancelsAcceptedWorkerBeforeWaitingForItsCompletion',
 'ServerTerminalJobStatusDoesNotMeanWorkerIsQuiescent',
 'RunOnceCompletionCanSignalWithoutAnyWorkerStarting',
 'AlreadyRetrievedMessageCanDispatchAfterShutdownTokenWasCancelled',
 'EphemeralSkipsSecondDispatchButDoesNotProveNoSecondAssignment',
 'RunOnceSignalDoesNotSkipShutdownCleanupBeforeSessionDeletion',
}
results = root.findall('.//t:UnitTestResult', ns)
actual = {r.attrib.get('testName', '').rsplit('.', 1)[-1] for r in results}
if actual != expected or len(results) != len(expected) or any(r.attrib.get('outcome') != 'Passed' for r in results):
    raise SystemExit('Exactly six compiled lifecycle contract tests must execute and pass; discovery/no-test success is insufficient.')
print('Six ACTUAL pinned runner method tests passed. No job fence, installed-image qualification or release authority granted.')
PY
