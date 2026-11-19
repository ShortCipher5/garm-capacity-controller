# Operator visibility of non-pool overhead

The existing overview now exposes only two canonical resource subtotals from
the same SQLite snapshot as global/host charges: retained native domain overhead
and retained shared artifact-root overhead. These are already included in totals,
not extra allocations to add again. They do not occupy runner slots or belong to
a repository pool. Conservative domain ceilings can overlap guest charges.

The overview displays a compact summary and expandable accounting details beside
the existing global capacity metrics. This is not measured host or disk usage,
per-guest/per-host attribution, proof material, a cleanup action or a release
authorization. Old servers without the field show unavailable rather than zero.

Projection and client validation reject negative, overflowed, runner-attributed,
artifact CPU/RAM, or combined overhead greater than canonical global charges.
No transport descriptors, paths, credentials or native identities are exposed.
