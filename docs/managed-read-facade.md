# Closed managed read facade

`internal/managedread` supplies only GET instances, pools, jobs and an explicitly
scoped individual pool. It consumes the existing `managedprojection` functions;
the existing writer consumes those same functions unchanged. No settings,
journal, inspector, credential metadata, login, bootstrap or mutation route is
present. An immutable exact verified client leaf gates every read, and caller
scope slices are copied. Mutation methods are refused before body/source access.

A typed `Source` supplies those four reads and source/controller identities. The
facade pins the pair at construction, checks it before and after reads, and
permanently refuses subsequent reads after any identity mismatch. It neither
forwards headers nor accepts a generic request/transport/URL callback. Unknown
provider-owned inventory/pools remain visible under the original projection
rules; single-pool reads require explicit enrollment, and jobs are repository
scoped. Projected data is not a capacity or release permit.

The real `NewManagedInventory` and `NewManagedDemand` clients are exercised over
loopback TLS with generated distinct server/client keys, verified CA chains,
server SPKI pinning and the facade's exact client leaf. Their upstream is a typed
synthetic source. These tests prove protocol interoperability and projection,
not successful stock authentication or topology admission.

## Optional service and isolation

`capacity-stock-reader` is a separate Linux executable for the read facade. It
takes the path of one owner-only JSON file as its only argument. Expected
fields are `listen` (literal loopback address and nonzero port), `stock_endpoint`
(private literal HTTPS endpoint), `stock_controller` (stock UUID),
`stock_server_key_pin` (lowercase SHA-256 of stock TLS leaf SPKI),
`stock_tls_name` (the verified server name if it differs from the socket IP),
`stock_ca_file`, `stock_bearer_file`, `inventory_tls` (CA, certificate and key),
`gateway_leaf` (lowercase SHA-256 of the exact gateway certificate DER),
and nonempty `providers`, `pools`, and `repositories` allowlists.

Stock v0.2.1 inventory uses a stock admin bearer. The upstream code only
makes fixed GET requests and never forwards the bearer to the gateway, but
stock does not enforce GET-only privileges on that bearer. The reader must run
under its own OS/container identity. Mount neither its config directory nor
its bearer, trust configuration, server private key or stock state in the
gateway. Configure the gateway for managed proxy inventory with its own
client identity, trust and reader SPKI pin, no stock token, and provisioning,
management and automatic mutations disabled. Its HTTP interface needs a
separately authenticated private operator route, not a public proxy.

Build the separate `stock-reader` target, verify the immutable image digest,
and explicitly opt in with `deploy/compose.stock-reader.yaml` as a separate
Compose project only after review. This project declares no stock service or
dependency and publishes no ports. Both peers reference the same exact,
independently verified existing stock container ID; they must actually share
its network namespace, not simply a similarly named bridge. The reader and
gateway mount different owner-owned directories and run as different non-root
identities. Never give either a stock data/config mount or a Docker socket.

Select distinct unused loopback ports. Confirm the resolved image digests,
mounted paths/owners and exact TLS peers, separately verify the existing
private HTTPS route, and check the Docker-level isolation in a disposable
setup before production. The namespace and port layout do not create an
HTTPS route to stock: an existing plaintext listener is not a substitute.
Transport destination restriction is application behavior, not network
enforcement. If stock is replaced, stop the companion, re-verify the exact
new identity, endpoints and trust, and explicitly recreate only the companion
against the new stock container ID. Do not assume it automatically follows
the stock name. Do not restart stock to start, update or remove the companion.

For bearer replacement, independently obtain new credentials with the approved
stock administration procedure, atomically replace only the owner-readable
reader bearer file, and restart only the reader. Its existing process never
auto-refreshes or resumes on rejected credentials; no new stock login is done by
the reader. A server-key or expected identity change requires deliberate
re-enrollment. To roll back, stop the optional reader and gateway only and
remove only their separate private operator route; do not alter stock or its
job/runner callbacks.

The local joined tests use the actual gateway inventory/demand clients against
a synthetic stock API, so they establish protocol and fail-closed behavior,
not deployed credential validity or a qualified production topology.

### Stock HTTP inside the approved container namespace

The optional `compose.stock-reader-http-bridge.yaml` adds a separate
loopback-only Caddy process to the companion project; it does not alter the
existing public proxy or inject a route into stock. Its fixed site name
`stock-read-bridge.invalid` is used only for SNI and HTTP Host verification.
No DNS record is needed: set the reader's `stock_endpoint` to the exact
literal loopback address and `stock_tls_name` to that enrolled name. The
bridge certificate must have that SAN and the reader must pin its SPKI as
well as verify the certificate chain. Specify both `stock_client_certificate`
and `stock_client_key` in the reader's private file. The bridge must verify
the issuing CA and that exact reader client certificate; it needs no reader
client key, GARM token file, signing key or stock data volume.

Use distinct owner-only mounts for all three roles; the bridge stores its
TLS key and public reader trust, the reader stores its distinct client key
and the bearer, and the gateway stores only its own identity. The sole
plaintext hop is fixed to stock on 127.0.0.1 in the same verified container
namespace; no such hop is allowed across a Docker bridge or host network.
A shared stock namespace is a trust boundary, not protection from a
privileged Docker host or another container with network-debug permissions.
Only mount the source-reviewed bridge Caddyfile. Disable its admin endpoint,
automatic redirects and access logs, and verify exact GET scope before use.

Start the bridge first and verify readiness and the reviewed TLS identity,
then the reader, then the gateway. Stop them in reverse order. Rehearse
stock recreation on disposable resources; a new stock container ID
requires explicit reattachment and identity checks on all three. Never
restart production stock to install, upgrade, or roll back the companion.

The UI sends the app read bearer in Authorization and deliberately omits
ambient credentials for its API calls. Qualify the actual private operator
VPN/tunnel or equivalent with both authenticated overview data and a denied
request from outside the trusted path. Do not put a hostname-wide Basic
Auth or a cookie-dependent access gate in front of it without proving browser
data access and the corresponding denial; visible HTML alone is not enough.
