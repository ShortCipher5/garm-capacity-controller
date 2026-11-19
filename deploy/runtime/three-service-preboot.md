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
Ingress currently uses the existing default SIGTERM exit (143); the gateway
must exit cleanly (0). Neither a killed/OOM process nor a readiness timeout is
accepted as a pass. Failure cleanup only removes this invocation's names and
generated fixture directory. Public results contain image identities and bounded
booleans, not credentials, config contents, raw process IDs or namespace IDs.

Offline tests check generated config separation and assertions only. The actual
three-service start is unproven until this exact candidate passes the hosted job.
Existing protected Observe, bootstrap, Session, providers and restart code are
untouched; this fixture cannot produce their authority objects.
