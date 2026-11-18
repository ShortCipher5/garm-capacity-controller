# Authenticated discovery to a saved native configuration

`--allow-native-discovery-planning` enables a separate token/confirmation flow on
existing SSH inventory. Private `admin.native_planning` contains `token_file`, an
existing protected `selection_directory`, and a maximum of8 explicit `bindings`.
Each binding pins a fresh PreparationID, policy Host, the full inventory target
(including current review digest/key) and the approved native administrator target.
This pairing is a deployment-approved association, not proof that two endpoints
share physical hardware. Neither equal IPs nor OS inventory establishes that.

GET `/admin/api/v1/native-planning` returns target IDs, bindings and binding digests.
POST `action:probe` requires `target_id`, `binding_digest`, `request_id` and
`confirm_read:true`. The real SSH inventory service durably audits and executes its
fixed pinned read. Target identity is rechecked before and after. The service holds
the actual result privately; browsers cannot submit replacement observations.

The proposal has an original observation digest,15minute expiry, exact review
digest and suggested Choice. Defaults reserve10% CPU and20% RAM (normally at least
2GiB, limited to half small hosts); available RAM further constrains the suggestion.
Runtime bootstrap/daemon/monitor budgets are included rather than allocating all
parent resources to guests. Tasks limits are configuration defaults, not discovered
OS capacity. Users may adjust the Choice, including reserves; positive finite
partitions and parent+reserve within observed OS totals are mandatory. Low buffers
and current memory pressure need visible warnings. Existing ledger reservations,
other workloads and global ARC memory are not certified by this calculation.

POST `action:save` requires target_id, review_digest, exact Choice and
confirm_save:true. It uses only the retained fresh observation and rechecks current
inventory identity. It persists a fresh0600 Selection file with immutable source
identity/digest and selected real Limits/Partition. The response echoes target/review
and the exact selection digest. This is `saved_configuration_not_applied`, never
capacity enrollment, physical authority, preparation or activation. At most64
selections per preparation ID are retained; files are not overwritten or erased.

A failed response can mean a file was saved. Explicit POST
`{action:"history",target_id:...}` reads the actual bounded protected files and
validates their binding/content digests, allowing lost-response recovery without
automatic repeat saves. Historical expired records remain visible but cannot pass
the bundle builder's freshness requirement.

The offline native bundle builder consumes the saved Selection plus an independent
approved Binding file and applies only Limits/Partition to the actual capsule
configuration. ZFS root/GUID/quota, runtime image, networking and private TLS remain
explicit deployment inputs. The resulting new bundle needs exact review/reload in
the preparation registry. This slice does not automatically rebuild, reload, install
or activate it. A consumed preparation ID cannot be reused for another install.

Tests use a disposable local SSH server, real pinned transport/audit, Proposal→Save
and protected file verification. No live host is accessed or modified.
