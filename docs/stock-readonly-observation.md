# Protected stock-read-only pilot qualification components

The private `observeStockReadOnly` implementation is a bounded diagnostic
snapshot, not a permission, durable namespace seal or authenticated session.
The observation itself exports no runtime test hook, conversion to managed
evidence, namespace opener, login/bootstrap, writer or restart operation. The
explicit bounded qualification caller is described at the end; no normal
production service calls this path.
Existing managed verification is unchanged.

It reads only `/etc/capacity-stock-readonly/reviewed-profile.json`, with protected
root-owned ancestors and a private regular root-owned file. The fixed schema
`stock-readonly-profile/v1` requires the verified stock reference, linux/amd64
platform, all three distinct container IDs, distinct exact image IDs and all
three executable hashes. Stock image/hash are fixed to #239 measurements.
Gateway/ingress values must come from independently reviewed **actual v2** hosted
artifacts. No synthetic test value is a usable deployment pin.

The directory field binds only a protected local layout with the fixed #248
fixture/private, fixture/state, joined/gateway-private, joined/state and
joined/ingress-private subdirectories. Their parents must be root-controlled;
role directories must be owned by the exact nonroot role and mode0700. The
ordinary runner-owned hosted temporary layout is not thereby admitted. No
profile is installed by this change.

Actual observation uses the existing private root-socket/peer-authenticated
inspect-only Docker reader, a five-second overall bound and bounded kernel/file
reads. Checks include fixed umask shell launch and actual executable argv, role
images/UIDs/capabilities/cwd/mounts, executable hashes/inodes, shared isolated net
namespace and separate non-host PID namespaces, no published ports, no other
running Docker namespace member, exact four IPv4 loopback listeners, repeated
process lifetimes/namespaces/container descriptions and reviewed-profile equality.
Membership is checked again. Failure returns no observation.

These checks are sequential, not atomic. A trusted privileged host actor can
change namespace membership, listeners, files or processes between samples or
after return. Host processes entering a namespace are not an exhaustive Docker
membership inventory. The result therefore has no accessor or consumer that can
open a namespace, authenticate, grant authority or promise continuing isolation.
Before a future consumer, that consumer's temporal trust boundary must be reviewed
explicitly, along with actual root-owned mount layout and hosted runtime results.

Tests inject only descriptions into pure checks and exercise canceled/nil calls
before I/O. No test launches Docker or successfully invokes protected observation.
Root-owned protected measurement, actual v2 artifact review, authentication and
Menkent qualification remain separate prerequisites.

## Held listener comparison primitive

The private listener-set comparison now distinguishes four-port preboot from a
five-port description only when a retained accepting IPv4 TCP socket has exact
address `127.0.0.1:9445`, no reuse-port setting, and an inode matching the table's
single extra listener. Its actual descriptor identity is checked before and after
comparison; closing or replacing it invalidates the comparison. Tests create only
local disposable sockets and synthetic tables, not a protected runtime.

The actual observer still calls only the four-port branch. No supervised observer,
namespace-opening capability, server, authentication or session is added. A later
sealed lifecycle must establish the provenance of the namespace table and socket,
retain and revoke the listener, and prevent descriptor handoff. This comparison
cannot establish sole custody, atomicity, or immunity to privileged duplication or
changes between/after samples. It is not an admission or connection permit.

## Retained preboot identity

A separate private constructor now brackets retention of the actual observed
stock network nsfs descriptor with two complete protected preboot observations,
within one five-second context budget. It compares exact role PIDs, process start
times, PID/network namespace identities and protected profile, plus nsfs inode.
The descriptor is neither exported nor usable through an accessor. Recheck repeats
the actual preboot observer; any failure closes and clears the retained object.
Explicit close is idempotent. Tests use canceled contexts, ordinary disposable
files and pure lifetime comparisons, not successful injected runtime admissions.

This preserves a namespace reference, not a live process guarantee. It does not
freeze processes or make sequential observations atomic. The holder alone remains preboot-only: adding a fifth listener causes its
preboot recheck to fail and revoke. Only the separate private lifecycle below
owns the supervised transition; the holder exposes no namespace capability.

## Private supervised lifecycle candidate

The private lifecycle now consumes the retained holder, rechecks exact preboot,
then creates only `127.0.0.1:9445` on a dedicated locked OS thread in that retained
network namespace. It verifies entry and restoration against actual namespace
inodes; restoration failure closes the socket and exits the still-locked thread,
so that thread is never returned to the Go pool. The constructor joins the worker
before releasing the namespace holder. It then runs the full observer with the
actual held accepting socket identity and exact five-port table, retaining the
same role lifetime/profile identity. Drift, cancellation or failure revokes both
socket and binding. No Accept/Serve, credentials, session, exported handle or
production caller exists.

Local tests cover only refusal, close/revocation and race-safe lifetime cleanup;
no namespace transition or lifecycle bind has been executed. Cross-namespace
creation/restoration and cancellation still require separately approved disposable
qualification. The context deadlines bound context-aware operations, not mutex
acquisition, synchronous filesystem/hash operations or kernel calls. Cancellation
cleanup can therefore wait for an in-progress recheck; it is not a hard real-time
revocation guarantee. Sampled checks still cannot exclude privileged interference
or prove exclusive socket custody.

### Fixed existing-lineage prerequisite (no authentication)

The private lifecycle now additionally requires the reviewed pilot directory's
`supervisor-admin` directory, under root-controlled ancestry, owned by root with
mode 0700. Its five fixed root-owned 0600 single-link regular files are
`stock-auth-binding.json`, `stock-api.der`, `bootstrap-attempt.json`,
`bootstrap-record.json`, and `bootstrap-user.json`. Reads are bounded,
non-following and nonblocking, and bracket the complete file set. The canonical
binding record (`stock-read-auth-binding/v1`) commits `GarmID`, `LeafSHA256`,
`AttemptSHA256`, and `RecordSHA256`; hashes cover exact DER/canonical JSON bytes.
The existing attempt and record must agree with the container and actual
credential hash using the existing bootstrap grammar. Public commitments and
controller identity is retained, never credential bytes or tokens. UserID must
be empty, matching the actual existing authentication record producer; stock
provides no authenticated current-user read. Nonempty inferred IDs are refused.
Drift during lifecycle recheck revokes the listener and retained binding.

This does not authenticate the DER certificate against a live server, assert
certificate validity/trust, create an account or repair any missing record. A
future fixed TLS exchange must verify the actual peer against this commitment
and appropriate trust before login. No setup writes are supplied here. The
uninitialized disposable specimen has none of these records and must refuse;
only synthetic offline lineage transitions are tested. Existing bootstrap and
writer/session paths are unchanged.


### Private existing-record-only read session candidate

The separate private session consumes the supervised lifecycle and the same
bracketed five-file material. Its constructor performs only the existing fixed
login and controller-read protocol, with exact whole-record equality before and
after, and no first-run, repair, record creation or renewal branch. TLS 1.3 uses
normal certificate validity/hostname verification plus the reviewed exact DER
leaf. Each exchange is restricted to the fixed stock origin in the retained
namespace and bracketed by lifecycle/lineage verification. There is no public
transport or arbitrary request interface. After construction only Instances,
Pools, Jobs and UUID-selected Pool reads, opaque source identity and Close are
available on the private type. Errors revoke the token, listener and holder.

Tests use the actual authentication protocol with synthetic responses, ordinary
local TLS for expiry/name/leaf checks, and refusal/closed-state cases. They cannot
inject a successful protected lifecycle/session. No actual stock login,
namespace dial, deployment, authentication readiness or serving is qualified.
The separate fresh preboot specimen still cannot construct this existing-record
session. No production consumer or facade is wired.

## Private read service composition

`serveStockReadOnly` is reached only by the separate explicit qualification caller,
not a normal production service. It composes only the
concrete existing-record session, its owned fixed loopback 9445 listener, and the
GET-only managed read facade. It cannot accept a listener, handler, transport,
URL, scope or credential callback. Initial scope is fixed to provider `capacity`,
with no repository or individual-pool enrollment; this permits inventory under
existing projection rules, not queued repository demand or settings access.

The exact verified pilot directory's `supervisor-admin` must already contain
root-owned mode0600 regular single-link files: `read-serve.json`,
`read-serve-ca.der`, `read-serve-server.pem`, `read-serve-server.key`, and
`read-serve-client.der`. The root0700 directory and ancestors are verified;
nonblocking/nofollow bounded reads and repeated file/directory identity checks
reject incomplete or changing material. Nothing is created or repaired.

The canonical `stock-read-serve/v1` JSON record binds GarmID and ControllerID to
the actual authenticated session; CASHA256, ServerSHA256 and ClientSHA256 pin DER
certificates. GatewaySPKI and IngressSPKI are separately reviewed public role
commitments, not inferred from the preboot fixture placeholder. CA, serving leaf,
gateway client, stock server, gateway server and ingress server keys must all be
distinct. The dedicated CA is not stock-upstream trust. Normal chain/time/server
loopback-name/EKU validation precedes TLS1.3 client-CA verification plus exact
client-leaf matching. Missing/unreviewed commitments refuse startup.

Cancellation, owned lifecycle loss, serving error, observed topology/auth drift
or material change terminates the service; there is no automatic recovery.
A one-second periodic check uses bounded five-second observations and closes
active HTTP connections on termination and joins the Serve loop, not a separate
all-handler wait group. In-flight source operations retain the
existing bounded exchange and session close behavior. This is sampled drift
revocation, not a promise that privileged changes are atomically excluded.

Local tests use synthetic generated certificates and invalid/canceled contexts;
they do not install protected material, authenticate stock, create a namespace,
or exercise successful privileged service startup. That runtime path remains
unqualified; the qualification command has not been invoked.


## Explicit qualification caller, not production activation

The separate `capacity-stock-read-pilot` command accepts exactly one verb,
`serve-existing-readonly`. No arguments, extra arguments, paths, flags, address,
scope or duration controls are accepted. It is not added to any default image,
installer, service unit or existing managed-garm/writer command. There is no
host/runtime invocation as part of this implementation.

Its only package entry accepts a context, imposes a fixed ten-minute context deadline,
and calls the private fixed read service. SIGINT/SIGTERM cancel that context;
the deadline initiates cancellation, with existing bounded cleanup potentially
extending exit beyond ten minutes. Normal deadline/stop/failure emits a fixed unavailable/stopped diagnostic and
exit1, not a success or readiness certificate. There is no automatic restart,
repair, credential creation or fresh-bootstrap fallback.

A separately reviewed completed existing bootstrap attempt/record (empty UserID)
and explicit `stock-auth-binding.json` commitments must already exist. Bootstrap
alone does not create this review or the dedicated serving roles. The session
currently performs existing-record authentication before serving-role validation;
a malformed serving configuration may therefore cause a login followed by refusal,
never first-run, repair, authority or a served endpoint. No secrets are logged.

Tests of this caller deliberately exercise only rejected arguments and invalid or
already-canceled contexts. Successful protected startup, real signal-driven host
shutdown and the ten-minute runtime ceiling require later disposable qualification.

### Separate explicit fresh-pilot bootstrap (currently uncalled)

`bootstrapStockReadPilot` is private code for a later, separately approved
setup invocation. It uses only retained **exact four-port preboot** identity,
not the supervised listener/read session. It reads the same fixed protected
`supervisor-admin` location; it creates neither that directory nor credentials.
The provisional canonical `stock-bootstrap-binding.json` must have version
`stock-pilot-bootstrap/v1`, exact `GarmID`, `LeafSHA256` over `stock-api.der`, and
`CredentialDigest` using the existing generated-credential grammar. This
provisional record is not a completed `stock-auth-binding.json` and cannot
satisfy read admission. Normal TLS chain/time/hostname checks plus exact leaf
pinning apply to the fixed 127.0.0.1:9998 endpoint.

The existing bootstrap protocol is reused through a private read/create store
interface; its old writer implementation is unchanged. The pilot store uses
bounded no-follow/nonblocking reads and exclusive fsynced attempt/record writes.
The durable attempt is created before the sole first-run POST. A lost response
never permits another first-run: a later explicit invocation can only try the
same credential login and controller GET. Concurrent exclusive creation losers
fail without first-run. Partial/uncertain records remain in place, never removed
or repaired. Returned real records have an empty UserID, not JWT-derived data.
Only those three exact routes exist; there is no settings/provider/read-facade
route, automatic invocation, token output, listener9445 or completed-review
creation. Retained topology and protected provisional inputs are checked before
and after each exchange. Shared fixed dialing verifies thread namespace entry
and restoration, joins the worker and never returns a generic namespace handle.

Tests exercise the protocol with synthetic responses and temporary local files,
not a pilot account, Docker, setns or stock authentication. Real setup still
requires an explicit reviewed preparation/invocation and separately reviewed
completed commitments before opening an existing-only read session.
