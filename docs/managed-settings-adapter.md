# Managed supervisor settings from the existing console

The gateway can use `admin.garm_management.managed_supervisor=true` with the
same managed-proxy endpoint, server SPKI pin and controller ID as its inventory
source. Configure `proxy_server_key_pin`, `controller_id`, fixed `providers`,
`pool_ids` and private `tls` files. Existing separate admin management and GARM
write opt-ins control whether reductions are exposed. No stock token, repository
write scope, conditional API, local journal, or asserted routing/writer booleans
are allowed in this mode.

The gateway's concrete pinned-mTLS client reads supervisor settings/status and
calls only its strict positive maximum-reduction endpoint. The supervisor keeps
the sole durable settings journal; the gateway does not recreate one. Source or
controller drift closes the client. A possibly sent request with a missing or
invalid response blocks further client writes; reads cannot clear that latch.
There is no automatic request retry, pending resolution, or reset endpoint.

The console receives `maximum_reduction_only:true`. Pool settings remain visible,
but the adapter rejects every field except maximum, and rejects zero, equal or
increased maxima and values below current minimum idle. Repository balancing,
enable/disable, priority, labels and minimum-idle edits are unavailable. Pools
already at zero or their smallest valid maximum cannot be reduced through this
path. This does not retire runners, release capacity or implement upward scaling.

The actual root-managed topology and single-writer boundary still validate every
stock request. This adapter is not permission to use arbitrary existing stock
installations in write mode. Direct installations retain their separate existing
read-only/adoption workflow. No host was deployed or modified while implementing
this code.

### Read-only historical receipts

The controller role can read `/v1/managed/history`: up to 25 recent current-pool/provider-scoped journal receipts, bounded to 512 KiB. The supervisor projects typed settings fields, never raw dispatch data or unknown JSON fields. The admin overview uses the existing history list and sets `managed_history_available` only after a successful authenticated read. Unavailable and truncated history are not evidence that an operation had no effect.

Confirmed receipts describe the original request, not present settings or physical capacity. Reading history never clears a client uncertainty latch or the supervisor's unresolved journal slot, never retries a PUT, and exposes no reset/replay operation. A different confirmed request cannot resolve an unknown request.
