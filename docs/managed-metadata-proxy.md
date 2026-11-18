# Managed metadata credential reads

The authenticated private supervisor offers only POST
`/v1/managed/metadata/{pool UUID}/{runner name}/{file}` with a strict
`{"instance_token":"..."}` body. Supported files are `runner`, `credentials`,
and `credentials_rsaparams`. This is a read operation, not provider dispatch.

Before reading a requested credential it obtains the real private stock pool
and pending instance record with the supervisor credential. Pool, repository and
provider must be in deployment scope. It reads `.runner` with the supplied
instance JWT, requiring exact name and positive agent ID matching that pending
record and ephemeral registration. The requested bytes are returned unchanged
only after a second matching `.runner`, instance and pool read. This brackets
reads but does not claim atomic upstream snapshots. Each private stock request
retains topology reobservation and actual stock TLS pin verification.

Instance JWTs are never substituted for supervisor admin authentication, persisted
in a journal or logged here. Supervisor credentials are never sent to metadata
routes. Responses carry the same authenticated source/controller provenance as
managed demand, no-store, and no secret-bearing error body. This route does not
activate native writes, authorize creation, or prove original JIT preparation.
Actual root namespace/Docker qualification remains outstanding.

The scope check depends on the pinned stock v0.2.1 contract, not client assertions:
`auth/instance_middleware.go` verifies the HMAC JWT and resolves its required
`claims.Name` via `store.GetInstance`; `database/sql/models.go` gives instance
names a global unique index. `runner/metadata.go` obtains JIT files from that
validated instance's stored JIT configuration. Matching the returned `.runner`
name and agent ID to the root-read pending row therefore ties the token to the
allowed pool/repository. The client additionally checks the intended GitHub URL.
A future stock version changing this lookup contract requires requalification.
