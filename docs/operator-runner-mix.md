# Desired runner-size mixes

The existing Profiles screen can read and atomically save several global runner-size definitions with desired counts for one configured server. Counts are advisory configuration, not observed runners or capacity reservations. Saving does not register pool mappings, change pool limits, issue grants, or scale workers.

Select a server, read the current mix, edit the resource dimensions and counts, then review the complete replacement before supplying the management token. Profile names are global: replacing their definitions may make another saved mix stale. Existing immutable pool mappings keep their captured dimensions. Counts may be zero; every size requires positive disk and RAM and whole vCPUs.

The server checks both the saved mix revision and the ledger sequence. An intervening edit requires a new read/review. After a lost or invalid response, read the actual saved mix; the browser never automatically repeats a write. Closing a review or changing server discards pending responses and credentials. Desired totals are not a guarantee of physical fit or deployment readiness.
