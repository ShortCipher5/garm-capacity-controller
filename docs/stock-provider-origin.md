# Stock-provider-origin ingress (library contract, not deployment)

This contract admits a stock GARM external-provider `CreateInstance` invocation
**after** stock GARM has prepared its runner/bootstrap data. It does not promise
pre-JIT admission, benign native deferral, a fork, or original-preparation proof.
The existing prepared `/v1/create` path remains unchanged.

`gatewayrpc.NewStockHandler` is a separate handler serving only
`POST /v1/stock/create`. A verified TLS 1.3 client chain and exact leaf SHA256
fingerprint are required. Each leaf is configured for exactly one controller,
pool, and provider; there are no wildcard or request-supplied authorities.
The constructor takes the existing prepared handler and compares its actual
ordinary and preparation-source leaf allowlists; duplicate use is rejected. This library does
not start a listener or activate production provisioning. Client rotation is
not authority to adopt old dispatch origins.

Request body is strictly decoded stock `BootstrapInstance`, bounded by the
existing RPC limit. Host, capacity, backend, image fingerprint, and profile
selection come from trusted gateway policy and the atomic ledger placement,
not caller fields. Flavor/image/OS/architecture must match policy. Extra specs are restricted to the pinned stock wrapper described below; custom
bootstrap debug/package options are rejected.

The immutable digest version `stock-provider-v1` includes controller, provider,
runner name, pool, flavor, image identifier, architecture, OS, JIT mode, repository,
callback and metadata URLs, runner group, sorted labels, ordered public SSH keys, CA bundle,
boot-update policy, and the selected tool artifact identity (URL, OS, architecture,
filename and checksum). Only the renewable tool download token is excluded.
URLs must be HTTPS
without userinfo, query, or fragment. It is an identity digest, not a hash of the
secret bootstrap payload. Instance tokens, tool download credentials, and
rendered userdata are never persisted or logged by this path. Renewing a token
does not authorize another physical create.

New admission requires an existing unused, eligible broker grant matching the
exact authenticated authority, server-owned execution profile, resources and
host binding. It atomically consumes that charge into the actual runner
reservation, without double counting. There is no ungranted fallback. Existing
exact request replay does not consume another grant.

Admission binds the origin atomically to the reservation. Dispatch uses the
canonical ledger, not a separate queue with independent cancellation semantics.
A durable intent precedes the physical POST; received operation correlation is
persisted before waiting. Timeout, lost POST response, cancellation, and unknown
outcomes remain charged and never generate a second POST. Confirmed repeats
require matching origin/digest, binding, and live exact resource readback.
Static error responses never reflect backend errors or bootstrap content.

This does not supply exclusive host mutation ownership, protected runner/job
completion, authoritative retirement, or a qualified production environment.
Existing reconciliation and physical lifecycle fences remain in force. No
physical release or deployment is added.

## Wiring and remaining activation gate

The service has a separate `--allow-experimental-stock-provider` flag and
`stock_provider_scopes` configuration. The provider selects
`create_origin: "stock-provider-v1"` with separate `stock_tls` files for creates;
ordinary `tls` files remain for existing read/lifecycle calls. Prepared commands
are refused in stock mode. Default configuration continues to use the original
prepared protocol.

**Production startup currently refuses this flag**, and direct stock facade
calls against a runtime ledger also fail closed. No independently established
exclusive native authority exists in this implementation. The flag wires the
protocol, not proof of safety or an operator override. Offline disposable
fixtures can exercise the complete admission/dispatch path. Implementing the
protected sole-writer host authority is a real remaining coding item, not a test
or a configuration value to set to true.

## Pinned-stock bootstrap compatibility

Pinned GARM `154638445c3949c1958b01812f69d9a1e4d82684` calls
`MaybeAddWrapperToExtraSpecs` for normal creates. That function renders a
`runner_install_template` using the metadata URL and instance token and inserts
its bytes as base64 JSON. This path verifies exactly that pinned Linux default,
rejects extra keys/custom scripts, then renders a server-owned equivalent
wrapper which explicitly disables shell tracing and quotes metadata/token
arguments. The pinned upstream `set -ex` script is never executed here.
The bootstrap mode/version is included in the immutable command digest; only
the renewable token is excluded. Arbitrary custom install templates remain
unsupported. Fixture tests cover the pinned wire shape, not a live deployment.

Each stock leaf is scoped to one pool, while a plugin configuration has one
`stock_tls` identity. Multiple pools therefore need separate provider
configuration/aliases and leaves; one shared stock credential does not grant
cross-pool authority.
