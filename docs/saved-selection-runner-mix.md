# Saved discovery selection to editable runner mix

A separately authenticated native-planning `mix-proposal` action accepts only an
existing target ID, an exact saved selection digest and an explicit per-runner
`disk_mib` choice (1–1,000,000,000). The service reopens the protected selection,
checks the current configured inventory binding and the original 15-minute
freshness limit. It does not accept a browser-supplied selection or host resource
proof and does not probe the host again.

Proposals use the saved **configured** Guest partition, flooring memory bytes to
MiB. Fitting standard definitions are small (1 vCPU/2 GiB), medium (2/4) and heavy
(4/8). Start with one larger definition where it leaves room for a small runner,
then fill remaining configured CPU/RAM with small runners. All values are editable
review defaults, not performance recommendations or measured runnable capacity.
An undersized partition yields no invented minimum. Disk always comes from the
explicit operator input; root-filesystem observations never establish guest disk
capacity. `disk_fit_verified` remains false.

The result is `proposed_from_saved_configuration_not_applied`. This action saves
nothing. Applying the definitions/counts is a separate normal profile/mix review
and CAS write; actual pool mappings, grants, limits and runtime authority remain
separate. The proposal includes the exact host and saved selection digest to make
its provenance visible, not to confer authority.
