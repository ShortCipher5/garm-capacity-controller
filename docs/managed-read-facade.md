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

No runtime constructor, listener, service or command is wired to this handler.
The interface itself is not an authority object. The remaining pilot step is a
separately reviewed sealed stock source enforcing actual authenticated session
lifetime/topology on each typed read, plus validated listener/role configuration.
That source must not be substituted by caller-described evidence or a generic
proxy. Existing protected Observe and writer/bootstrap/restart behavior remain
unchanged; the hosted three-service preboot still has no supervisor upstream.
