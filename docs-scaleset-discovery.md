# Read-only existing scaleset inventory

The management overview optionally reads `/api/v1/scalesets` independently of
`/api/v1/pools`, with the same configured provider scope and trusted TLS client.
Scaleset local numeric IDs never become pool UUIDs or management targets.
No changes, enrollment, recreation, drain, accounting release or capacity grants
are performed. Owner metadata is not authorization and provider identity is not
a physical host observation.

A successful empty collection is displayed as no scalesets observed in scope.
Missing capability, unsupported endpoint, malformed data, cancellation or read
failure means unavailable, not empty. Pool and scaleset observations can succeed
independently; they are not an atomic snapshot. Existing durable write journals
and write guards remain independent. A conflicted edit keeps its original
settings while inventory and journal observations continue refreshing.

The optional scaleset read has a child deadline of at most three seconds and half
the remaining caller budget. Zero-target pool reads are separately bounded
the same way, reserving time for the other observation. No raw upstream error or credential is displayed.
Testing uses synthetic TLS fixtures; no deployed upstream capability is claimed.

If adding scalesets would exceed the existing 2 MiB overview response cap, the
whole optional scaleset observation is omitted as unavailable. Essential journal
and settings are retained; collections are never truncated or reported as empty.
