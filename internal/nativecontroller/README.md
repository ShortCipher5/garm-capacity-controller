# Dormant native controller coordinator

This package owns the canonical stock-provider dispatch callback and communicates
with one independently enrolled native owner over pinned mutual TLS. It refuses
runtime-lineage stores. It is not mounted by a production command.

The schema22 implementation coordinates stopped CREATE, immutable
accepted-operation observation, terminal observation, exact quota readback, then a canonical bootstrap plan, one-shot protected-file delivery and opaque
readback before START and its accepted/terminal observations. Each mutation is sent once. A lost
response, redirect, malformed receipt, timeout or uncertain outcome leaves the
canonical dispatch unknown and capacity charged. Read-only operation polling
never repeats an operation. The original prepared dispatch path is unchanged.

The START gate requires schema22 bootstrap readback, with no optional skip.
There is no production activation or release authority in this package. Loopback tests exercise real mutual TLS and SQLite against a
simulated native owner; they do not qualify a live Incus/ZFS host or prove
cross-controller fencing.
