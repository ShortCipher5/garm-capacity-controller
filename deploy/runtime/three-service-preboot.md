# Disposable three-service preboot

The existing hosted packaging job invokes the existing stock nonroot specimen
with `--joined-service` and the immutable IDs of its already-built gateway,
ingress and ledger-tool images. This is not a new deployment command. The
hosted-only checks, generated directories, stock startup/recreation assertions
and bounded cleanup remain in the stock specimen.

The additional phase starts the actual pinned stock executable as UID 65530,
actual ingress as UID 65531 and actual gateway as UID 65532, with explicit `/`
working directories. Stock retains its explicit **lab shell umask077** launcher;
this does not prove direct-launch private-state behavior. Stock has network=none;
the two children share its network namespace and retain separate PID namespaces.
No host ports, Docker socket, provider configuration, stock account, repository,
job or external API are introduced. All role material is freshly generated.

The generated gateway config uses the existing managed inventory client against
literal loopback9445. **There is deliberately no supervisor on that port.** The
actual gateway may serve its authenticated admin view, but failed reconciliation
must leave canonical admission unready. This is preboot evidence, not a working
stock inventory integration or a sealed authenticated supervisor. The fixture
also checks actual ingress rejects administration and forwards one fixed GET to
uninitialized stock, the actual processes/namespace separation, and inability
to reach their listeners from a separate networkless probe namespace. It never
contacts an external address.

Children are stopped/removed before the original stock recreation checks.
Both ingress and gateway must exit cleanly (0), stopped and non-OOM.
Ingress has a bounded graceful HTTP shutdown; failed drain is an error. Neither a killed/OOM process nor a readiness timeout is
accepted as a pass. Failure cleanup only removes this invocation's names and
generated fixture directory. Public results contain image identities and bounded
booleans, not credentials, config contents, raw process IDs or namespace IDs.

Offline tests check generated config separation and assertions only. The actual
three-service start is unproven until this exact candidate passes the hosted job.
Existing protected Observe, bootstrap, Session, providers and restart code are
untouched; this fixture cannot produce their authority objects.

The joined public result uses schema `disposable-three-service-preboot/v2`.
It additionally binds the exact ledger initializer image, actual checkout
`git rev-parse HEAD`, and hosted run ID/attempt. The checkout SHA can be GitHub's
synthetic PR merge commit; it is **not claimed to be the PR head**. These are
unsigned public provenance fields, not artifact signatures or release approval.
The fixed result is cleared before a joined rerun and emitted only after the
joined assertions, stock recreation checks, final stock removal and successful
fixture cleanup. A failed run does not retain an earlier joined success result.
No image archive, state snapshot, registry push or new upload is introduced.

V2 also records gateway/ingress executable SHA256 values measured from their
actual running PID1 executables. Each bounded role check requires the fixed
installed executable path and runtime executable to have matching inode identity
and bytes, with a 128 MiB maximum and 30-second outer timeout. Only the two
64-character hashes enter the public record, not command lines or mount contents.
This is a measurement of these disposable processes, not an admission or release
signature and not proof of a live installed stack.
