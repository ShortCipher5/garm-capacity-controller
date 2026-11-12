# Pinned runner lifecycle source contracts: no drain or release authority

This gate executes the **actual official Runner/JobDispatcher methods** from
[actions/runner v2.323.0, commit d8b82f44](https://github.com/actions/runner/tree/d8b82f44a9ec0258904d5869ae683c31d5aac299)
under mocked listener/service/worker boundaries. It changes no production runner
method source. One explicitly documented test-only dependency upgrade below means
this is NOT the exact production dependency graph. It does not install/register a runner, contact GitHub Actions services,
run a real job, implement a fence or authorize stopping/deleting anything.

This historical source pin is a counterexample/research fixture, NOT the installed
runner/image version. The actual installed artifact/source digest, broker/scale-set
behavior, native containment, supervisor/no-restart protocol, external writer
frontier and physical retirement remain unqualified. All current release gates
remain blocked. See [the physical proof boundary](../../docs/physical-retirement-proof-boundary.md).

## Six executable counterexamples and ordering checks

The overlay `RetirementSourceContracts.cs` is copied into the pinned upstream
xUnit project only in a new disposable checkout. No production method is patched.

1. Actual `JobDispatcher.ShutdownAsync` cancels a pending worker dispatch before
   waiting for it. This is not job-preserving retirement.
2. Actual `EnsureDispatchFinished` can cancel a still-pending worker despite a
   successful terminal server job result. Server terminal status is not native
   worker/post-job quiescence. Reflection exposes the actual private dispatcher
   record/method only in these tests, not a parallel implementation or receiver.
3. Actual one-shot dispatch can signal `RunOnceJobCompleted` after the first
   renew fails, without a worker starting or job completion being acknowledged.
4. Actual `Runner` can dispatch an already retrieved message after its shutdown
   token was cancelled. Token cancellation alone is not an atomic intake fence.
5. Ephemeral mode avoids a second local dispatch but can still retrieve a second
   assignment message and have another poll outstanding. This does not establish
   a service-side no-new-assignment frontier.
6. A one-shot completion signal does not skip dispatcher shutdown/cleanup before
   session deletion or local configuration removal.

All scheduling/worker handles are controlled in-process test doubles. These tests
verify selected source-method semantics, not real job processing, post-job cleanup,
server lease/session guarantees or a deployed-image safety property. They create
no reusable permission token and link to no ledger/facade retirement path.

## Six test-only local coordination experiments, not a production drain

`LocalIntakeFenceExperiment.cs` is copied ONLY into the disposable Test project.
It is NOT imported into runner production or controller assemblies. It has no
HTTP, registration, worker-cancellation, stop/delete, ledger or proof capability.
The [concrete accepted-work protocol](../../docs/accepted-work-drain-protocol.md)
defines the missing service/supervisor prerequisites and refuses production use.

Three new tests use the ACTUAL Runner loop with mocked listener/dispatcher seams;
three stress the test-only coordinator directly. They check late pre-seal poll
responses remain obligations atomically, premature RunOnce cannot stand in for
worker plus cleanup completion, cancelling an observation does not cancel work,
ambiguous poll/cleanup errors remain unresolved, and unresolved history cannot
be inherited by a fresh in-memory generation. A 100x/100-observation race checks
that poll resolution never exposes a zero-poll/zero-accepted gap.

Critically, a negative actual-Runner test shows that suppressing its shared message
poll ALSO suppresses legitimate cancellation/control delivery. Therefore this
experiment does NOT implement safe production drain. A qualified remote
no-new-assignment frontier plus preserved cancellation/renewal/control channel,
trusted native/descendant completion and no-restart supervisor are still absent.
Even `LocallyQuiescent` is only an in-process experiment result, not a service,
job, physical absence, old-writer or release fence. No production API accepts it.

## Exact remote-control and no-restart interface experiments

See [practical implementation options](../../docs/runner-fence-implementation-options.md).
Four new tests invoke ACTUAL classic/broker listeners with mocked transports. They
cover Busy status/control hints, late old-poll responses after local status-token
cancellation, and classic session recovery versus SkipSessionRecover. They do NOT
assert that the mocked transport represents an actual GitHub assignment policy.

Eight tests run exact unmodified RunnerService.js in a Node VM with FAKE child
process, signals and timers only. No real process is spawned or signaled. The
existing Node version is recorded; no Node/toolchain installer or dependency is
added. These tests show internal retry/update branches survive the unknown-failure
limit and an outer Restart=no, and service stop sends interrupt then delayed kill.
That is NOT a job-preserving stop or a qualified installed-image restart fence.
New C# interface cases are uncompiled/unexecuted until their own hosted result.

## No SDK installation

The local task workspace has no .NET compiler. Local Go/shell/source-hash checks
are **not compiled C# execution**. The dedicated hosted workflow uses the standard
`ubuntu-24.04` image's **already installed** compatible .NET 8.0.4xx SDK. The official
[image software report](https://github.com/actions/runner-images/blob/main/images/ubuntu/Ubuntu2404-Readme.md)
listed 8.0.427 when this gate was prepared. Upstream `src/global.json` requests
8.0.407; its normal same-feature-band patch selection must resolve an installed
8.0.4xx version. The gate logs the exact selected SDK and fails if it is missing.

There is no setup-dotnet, apt, SDK installer, upstream `dev.sh`, layout/external
binary download or fallback installation. Do not add one without authorization.
Only public-registry NuGet project dependencies are restored by the ordinary
build, with the narrowly scoped official security patch documented below. Build/tests run with a clean allowlisted environment and new
private HOME/temp/package directories; inherited runner JIT/input variables,
service tokens, proxy credentials and real home configuration are not passed. All upstream audit/analyzer/warnings-as-errors policies remain
unchanged. A historical dependency/audit build failure must be reported, not
silently suppressed to manufacture qualification.

The source version-constant target is invoked directly, then only these sixteen C# tests
are filtered and executed. Source HEAD and critical production-file SHA256 hashes
are checked before AND after execution. The generated version metadata must name
the exact source pin. TRX inspection requires exactly sixteen named Passed results;
an empty discovery/filter success cannot make this gate green.

## Explicit test-only security dependency baseline

The first hosted attempt selected preinstalled SDK 8.0.427, verified original
source hashes and generated version metadata, then FAILED before compilation or
test execution. Unchanged upstream auditing correctly rejected its original
`System.Security.Cryptography.Pkcs` 8.0.0 dependency for both advisories:

- [GHSA-2fmv-rv2c-59x7 / CVE-2026-70162](https://github.com/advisories/GHSA-2fmv-rv2c-59x7), moderate: affected 8.0.0–8.0.1; first patched 8.0.2.
- [GHSA-846f-f764-76mx / CVE-2026-84008](https://github.com/advisories/GHSA-846f-f764-76mx), high: affected 8.0.0–8.0.1; first patched 8.0.2.

Both official GitHub advisory records were checked, and the official NuGet
[nuspec](https://api.nuget.org/v3-flatcontainer/system.security.cryptography.pkcs/8.0.2/system.security.cryptography.pkcs.nuspec)
identifies version 8.0.2, author Microsoft, project dot.net and the dotnet/runtime
repository. No advisory, audit, analyzer or warnings-as-errors policy is suppressed.

Only for this disposable test build, `Directory.Build.targets` updates that one
PackageReference to 8.0.2. Original `Sdk.csproj`, Runner and JobDispatcher method
source stay unchanged and hash-verified. The resolved Test `project.assets.json`
must contain exactly the approved Pkcs version and record its nonempty resolved
hash (not a preapproved hash); a separate dependency-baseline
artifact explicitly records this TEST override and the two fixed advisories.
Other package/audit failures must still stop the build and be reported.

A resulting pass means selected historical method semantics were exercised under
this security-updated TEST dependency graph. It does NOT qualify the original
production graph, an original 2.323.0 binary or any installed runner/image. The
six original C# cases passed in [hosted run43216537181](https://github.com/ShortCipher5/garm-capacity-controller/actions/runs/43216537181) under preinstalled SDK8.0.427.
The six new local coordinator experiments remain uncompiled/unexecuted until their
own actual hosted result is observed.

## Run in an environment with an existing compatible SDK

```sh
integration/actions-runner-v2.323.0/test.sh /absolute/new-disposable-source
```

The source and all four sibling output paths (`.results`, `.home`, `.tmp`,
`.packages`) must not exist, including dangling symlinks. Every path is checked
before side effects and reserved with private, non-reusing mkdir operations;
races fail closed and partial reservations are preserved, never overwritten.
Artifact upload requires this invocation's successful fresh-output reservation,
so an early refusal cannot upload an old TRX or a symlink target.
The gate fetches only the exact public commit and
refuses to overwrite an overlay. Results and selected toolchain metadata are in
the new sibling `new-disposable-source.results` directory. No binary is packaged
or deployed; only TRX/toolchain results are uploaded by least-privilege hosted CI.

For an explicitly source-only check against an existing pinned checkout:

```sh
integration/actions-runner-v2.323.0/test.sh --check-source /absolute/existing-source
```

That command prints `Compiled .NET lifecycle tests NOT RUN`. It is not runtime
qualification. Set `GCC_PINNED_ACTIONS_RUNNER_SOURCE` to the explicit checkout path
when running the local Go source-hygiene test; the ordinary Go suite otherwise
skips that source-availability check. The hosted .NET workflow remains separate.
