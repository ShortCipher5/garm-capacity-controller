# Managed stock control-plane boundary (under construction)

No deployment or writer activation is performed by this source. Arbitrary remote
GARM URLs do not gain single-writer authority. A generated configuration is not
observed runtime evidence, and an isolation snapshot is not a permanent permit.

`capacity-managed-garm prepare PRIVATE.json` is root-only and creates a **new**
private directory. It refuses existing paths, rootless preparation and writable
parent directories. Separate private UID65530 GARM configuration/state, UID65532
controller configuration/state and UID65531 public ingress files are prepared.
The bootstrap/admin credential remains in root-only supervisor-admin, which is
never mounted into any service. Controller credentials are only scoped mTLS
client credentials for the privileged proxy, not stock admin credentials.
Fresh high-entropy JWT/database/admin secrets never appear in output or Compose
environment variables. Existing GARM databases/users/tokens are not adopted.

The generated Compose document is disabled behind `managed-control-plane`, has
no restart policy or host networking and publishes **only ingress port8443**.
Stock GARM binds TLS admin API127.0.0.1:9998; controller and ingress share that owned
namespace under separate non-root identities, read-only filesystems, no added
capabilities and no-new-privileges. No Docker socket is mounted in those services.
The operator must supply reviewed immutable local images, ingress TLS material
and initially read-only controller configuration. No stack is started by prepare.

`capacity-managed-garm inspect PRIVATE.json` is a root-supervisor read-only step:
it authenticates the local root-owned Docker socket peer, inspects exact pinned
container IDs/images, users, commands, namespace, ports, privileges and mount
separation; enumerates other running containers; and reads actual kernel network
namespace/listener state. Missing, changed or inaccessible facts refuse the check.
The opaque observation can open only its exact network namespace, pinned by
process start time and kernel inode, within five seconds. This descriptor does
not extend authority: the supervisor rechecks topology for each operation.
The trusted host root/container orchestrator is outside this isolation threat
model. A compromised root can replace containers, namespaces, credentials or code.

**Still required before activation:** independently reviewed combined bootstrap/proxy
startup, real stock-host qualification, credential renewal and native execution
authority. The source now includes first-admin recovery and an authenticated
pause-only proxy; neither permits capacity increases. The observer currently supplies no write permit. The rootless
unit tests do not qualify a real Docker topology or deployed stock GARM.

## Exact public route contract

Source: pinned stock GARM `154638445c3949c1958b01812f69d9a1e4d82684`,
[router](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/apiserver/routers/routers.go#L145-L208).

- POST `/webhooks` and the configured controller UUID suffix (optional trailing slash).
  Before first-admin initialization the UUID may be empty; suffix routing remains
  disabled until the actual discovered controller UUID is configured.
- POST `/api/v1/callbacks/status` and `/api/v1/callbacks/system-info`.
- GET the exact metadata endpoints: runner-metadata, runner-registration-token,
  credentials/single-filename, system/service-name, systemd/unit-file,
  system/cert-bundle, tools/garm-agent, tools/garm-agent/numeric-object-ID,
  tools/garm-agent/numeric-object-ID/download, install-script.

The stock file-object ID parser is numeric, not UUID. Metadata/callback routes
retain stock instance-token middleware; webhook HMAC headers/body are forwarded
unchanged for stock verification. The proxy never supplies management credentials.
No login, first-run, controller/admin API, UI, general prefix routing or OPTIONS
browser interface is exposed. Queries, escaped paths, path traversal, absolute
URLs, upgrades and method-override headers are rejected or stripped. Upstream
redirects are rejected rather than followed or exposed. Only literal loopback
HTTPS upstreams are required by the managed ingress command; public ingress is TLS.
The separately prepared certificate has IP SAN127.0.0.1 and its DER SHA256 pin
is recorded in the root manifest. GARM alone receives its private key; root and
ingress receive only public certificate copies. Credential-bearing root calls
verify the peer on every TLS connection, closing listener-replacement races.

This allowlist is source-reviewed and synthetic-transport tested, not yet a full
stock runner bootstrap/download qualification. Production remains disabled until
that full qualification and the independent enforcement review pass.

Managed images must have no baked environment beyond the fixed PATH. The observer pins actual Docker Path/Args, empty entrypoint, working directory / and that exact environment. Compose cannot erase arbitrary extra baked image environment entries; such images are refused. All privileged preparation/secret ancestors must be root-owned and not group/world writable.


Controller `garm.managed_proxy=true` uses explicit mTLS credentials and
`proxy_server_key_pin` to read inventory from the root proxy, without a stock
TokenFile. It refuses simultaneous token configuration, legacy settings writers,
and preparation/provisioning mode. Startup does not require the supervisor
already listening: unavailable inventory retains blocked admission and retries
through the existing reconciliation loop. The response projects only state and
identity; private job details become a busy marker and secret fields are removed.
Every authorized-provider row remains visible, including unknown pools.

This first controller integration is **inventory only**. Managed demand discovery,
enrolled source identity and capacity-increase dispatch remain separate work;
the existing grant cycle is not activated by this mode.
