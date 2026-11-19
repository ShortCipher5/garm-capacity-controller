# Read-only host memory planning, not activation

`capacity-native-memory-plan --reserve-bytes N --capsule-ceilings-bytes M`
reads actual procfs MemTotal and, when available, fresh ZFS ARC diagnostics.
N is an explicit combined allowance for the OS, services, ARC and other
uncontained memory. M is the sum of all planned whole-capsule hard ceilings,
not current usage and not a sum of guest requests already contained therein.
The command makes no kernel, ledger, service or admission changes.

The report rejects zero/invalid/overflowing inputs and detects physical
oversubscription using subtraction without overflow. A plan that fits still
reports `blocked_unbounded_host_memory`: configuring a reserve does not enforce
it. ARC `c_max` and `zfs_arc_max` are targets, not an all-ZFS-memory upper bound.
Even ARC size currently below the allowance cannot prove future safety. Missing
ARC diagnostics never produce a zero-usage assumption. A current ARC size above
the entire reserve is reported as an additional warning.

Successful report output deliberately exits 3, not 0, and always includes false
activation/release authorization fields. This is a planning tool, never an
opaque proof or readiness switch. Do not use its numbers to subtract canonical
charges, reduce a reserve automatically, enable native execution or release a
guest allocation. An enforceable complete host-global memory boundary or a
separately qualified deployment risk policy is still missing. No such policy
or production bypass is created here.

Tests cover parser/units, duplicate totals, numeric overflow, exact-fit and
overcommit arithmetic, and absence of authorization. They do not inspect a
live target or qualify a kernel. External system services and memory hotplug
remain deployment concerns; total-memory bracketing only detects observed drift.
