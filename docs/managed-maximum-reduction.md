# Lower a managed stock pool maximum

Pinned stock GARM v0.2.1 source reviewed:
- [UpdatePoolByID](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/pools.go): validates minimum against maximum, then updates stored settings; no runner deletion.
- [Pool lifecycle](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/pool/pool.go): creation checks maximum; idle scale-down uses idle runners, minimum target and queued jobs, not maximum. Existing busy runners are not selected because the maximum changed. This does not fence already queued creates or prove physical release.

`POST /v1/managed/pools/<pool>/reduce-maximum` requires the existing controller
mTLS role and scoped pool. JSON contains `request_id`, `expected_fingerprint`,
`maximum` and `reason`. Maximum must remain positive and at least existing
minimum idle, and be strictly below a fresh authenticated current maximum.
The root Session mutex serializes that final read and PUT; actual topology is
rechecked around both. No minimum-idle change, increase or re-enable is accepted.

The existing settings manager/journal records the original request before PUT.
Any possibly dispatched failure remains UNKNOWN across restart, even if later
reads match. A rejection during the final extra read may conservatively leave
an unresolved intent. No automatic replay, charge release or runner deletion
is added. This is an explicit reduction of future starts, not automatic
cross-repository capacity transfer or proof of deployment qualification.
