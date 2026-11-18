# Managed stock GARM bootstrap and pause proxy

This path keeps stock GARM unmodified. It is not a live deployment or completed
capacity-increase authority. Preparation/inspection and the private ingress are
separate prerequisites. The root supervisor alone holds the generated bootstrap
password; the controller container never receives that password or an admin JWT.

## Concrete commands

After the prepared, pinned containers are actually running, a root supervisor can
run `capacity-managed-garm bootstrap <private-boundary.json>`. It performs actual
Docker/kernel topology observation and reaches only the pinned namespace's
loopback stock API over TLS with normal CA/IP verification and the independently pinned DER leaf. The root-owned public trust copy is never read from a controller-writable path. A root-private `bootstrap-attempt.json` is created exclusively
and synced **before** the one first-run request. No retry, process restart or lost
response may send first-run again. Authentication with the same generated
credential can reconcile an uncertain result. A successful authenticated controller
read is bound into immutable `bootstrap-record.json`; credentials/JWTs are never
stored in these outcome records or printed.

`capacity-managed-writer <private-config.json>` opens only an already bootstrapped
session, an existing independently lineage-bound settings journal, and its configured
server TLS identity. It listens on port9445 on loopback in the exact observed GARM
network namespace. Its configuration is:

- `boundary`: the pinned managed topology specification;
- `tls`: existing private CA/certificate/key file references;
- `client_certificate_file`: root-private copy of the public controller certificate;
- `client_leaf`: the exact DER SHA256 leaf of the dedicated controller mTLS role;
- `journal`, `journal_lineage`: existing settings journal and independent lineage;
- `providers`, `pools`, `repositories`: explicit management scope.

Root config/TLS/client-certificate/journal paths require canonical absolute names and root-controlled non-writable ancestors. Controller, proxy-server and stock-server keys must be distinct, even if different certificates wrap one key.

The session uses a dedicated locked OS thread only while entering a pinned network
namespace for dial/listen. It restores the original namespace before unlocking. A
failed restore destroys that locked thread instead of contaminating the runtime.
It inspects actual topology before and after upstream operations, closes access on
observed drift, does not follow redirects or use ambient proxies, and does not
reuse upstream connections. Opening a successor does not clear journal ambiguity.

## Narrow API and real limitations

Pinned mTLS grants only the configured role. No browser/cookie auth, arbitrary
upstream URL, credential forwarding, login route, users API or native API proxy is
provided. The settings snapshot/status routes use existing bounded projections.
`POST /v1/managed/pools/<UUID>/pause` accepts an immutable request ID, reviewed
fingerprint, reason and `disable` and/or `zero_maximum` flags. It traverses the
existing durable intent journal and the existing stock settings client. Minimum
idle is never silently decreased: a zero maximum is rejected when it conflicts
with a positive minimum. Unknown/pending outcomes block new mutations. A pause
neither drains jobs nor releases capacity. Historical exact-request replay is not
a fresh observation.

Only safe pause writes are implemented. Enabling pools, raising maxima, changing
minimum idle, native provisioning or release require the real qualified authority
and lifecycle integration that this module deliberately does not invent. Endpoint
isolation and observed topology are not native resource ownership. JWT expiry
fails closed; re-open uses authenticated login, not a fresh first-run or new journal.
No stock credential is returned to the controller.

Protocol tests use disposable fakes for lost first-run responses and persisted
markers. They do not establish that Docker, network namespaces or a real deployed
GARM process have been qualified. Actual root topology and namespace switching
must pass on the supported host before this can be described as deployable.
