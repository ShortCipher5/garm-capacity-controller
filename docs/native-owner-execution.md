# Experimental surviving-owner execution path

This package is not wired into the production command or enabled stock runtime.
It is code under review, not a claim that installed hosts enforce exclusive
ownership. The read-only policy prerequisite is published separately.

## Execution order

1. Authenticate the exact pinned controller client on TLS1.3; validate the
   canonical envelope against the owner's immutable policy and body digest.
2. Permanently consume both permit and native name in this surviving owner's
   memory before any central exchange. The maps never evict; a fixed bound
   refuses additional executions. They are not a budget ledger.
3. Obtain the first canonical parent consume response over this owner's pinned
   mTLS exchange, then a first create_stopped suboperation claim from the same
   authoritative SQLite ledger. A detached receipt is never accepted.
4. Submit one private native POST with start:false. A lost response never causes
   another submission. Preserve the exact accepted operation UUID and name.
5. Serve immutable accepted evidence separately from actual terminal observation.
   A successful operation also requires the exact stopped instance/config/UUID.
6. Check the actual private storage pool and volume mapping, then execute only
   a pinned root-owned read-only ZFS get through a descriptor-sanitizing helper.
   Require exact filesystem quota bytes and dataset GUID, not a driver label or
   configured size. Recheck native state/identity around the root read.
7. The controller records accepted/terminal/quota observations in the canonical
   ledger. Only then can the owner receive a fresh START claim. Before that
   exchange it rechecks quota and instance identity. Submit a single fixed start.

No failure, timeout, missing operation, absent instance or zero process count
releases resources. There is no deletion/retirement endpoint. Unknowns remain
charged and a failed observation permanently stops that execution's progression.

## Owner lifetime

The actual fresh Domain can construct only one native Client, and that Client
can construct only one execution owner. Neither guard resets. The actual owner
process holds the birth pidfd. If it dies, the daemon remains quarantined: no
constructor adopts a directory/daemon or restores these memory tombstones from
caller JSON/PID. Controller restart can reconnect only to the same surviving
owner. Central raw-database rollback is not solved by a SQLite one-use flag alone;
owner tombstones also reject reused names/permits while that owner survives.

## Limits and missing integration

- Trusted root, native binary, helper, kernel and dependencies remain assumptions.
- Only pinned Incus6.0.4, default-project stopped filesystem-backed ZFS containers
  are considered. VMs, block-backed volumes and refquota-only storage are refused.
- Native operation/quota parsers and TLS fault paths have disposable tests. Actual
  root ZFS/Incus/systemd enforcement has not been exercised in this environment.
- The command does not provision a private ZFS pool/bridge, load deployment TLS
  and policy configuration, or serve execution. Controller orchestration must
  bind the create/observe/record/start sequence to the existing dispatcher.
- No protected job supervisor, trustworthy idle/drain signal, native deletion
  completion or release authority follows from successful create/start.
- Image/cache/daemon overhead and host reserves need an enforced host-domain
  resource policy; a per-volume quota is not proof of total host disk isolation.

Do not enable the production runtime based on this package's presence.
