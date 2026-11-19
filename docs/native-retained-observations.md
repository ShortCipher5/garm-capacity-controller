# Exact DELETE-claim retained observations

The authenticated owner GET
`/v1/native/retirement-operations/<delete-claim-id>/evidence?nonce=<64hex>`
joins only the original in-process handles retained for a canonical succeeded
DELETE. No mutation is submitted. Unknown operation history remains latched;
no recovery retry or imported receipt can reconstruct missing handles.

The native adapter reads the actual retained PID1 namespace termination, exact
native DELETE operation and API absence, then the original before-START storage
reference tracker and current pool/root accounting. It brackets storage work with
namespace and native absence reads and repeats the actual boundary check. The
join has a five-second original-start deadline. Namespace identity, native UUID,
domain/policy/name, DELETE operation, dataset GUID and canonical claim/request
identity are checked together. API404 never substitutes for kernel/storage reads.

The separate `nativeretained.Observe` transport owns its mTLS credentials, pins
the actual owner certificate/full descriptor, sends a fresh nonce to the exact
claim endpoint and requires byte-canonical bounded JSON. It compares the entire
canonical succeeded DELETE record supplied by its trusted caller and retains the
request-start monotonic timestamp. Expired samples, missing observations, changed
identities, false release claims and unavailable originals are rejected.

The response explicitly says `retained_observations_only` and
`release_authorized:false`. It is not physical completion: surviving historical
shared extents, host-global ARC/kernel memory and external service work are not
attributed by these observations. No schema refund, exemption, release method,
readiness flag or activation bypass exists. Full canonical charges remain.

Tests cover zero/missing private handles, exact route/read rules and a local
mutually authenticated TLS fixture with nonce/identity/shape/release/accounting
adversarial cases. They do not manufacture a live kernel/ZFS completion proof.
No real host or native guest was started, stopped or deleted for this change.
