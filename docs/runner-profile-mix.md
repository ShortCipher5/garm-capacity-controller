# Reviewed runner profiles and desired mix

Schema 28 adds advisory desired-mix configuration only. It does not implement
hierarchical accounting, exemptions, physical capacity, reservations or dispatch
binding. All existing reservation and overhead charges remain unchanged.

An authenticated operator can atomically save up to 32 named size profiles and
counts for an existing host. All profile definitions and the desired mix commit
together. The global audit sequence and mix revision must still match the review;
intervening profile, policy or accounting activity causes a conflict. A lost
response requires a fresh read rather than automatic replay.

Profile names are global. Applying a reviewed batch replaces those definitions;
other saved mixes can subsequently show `profiles_match: false`. Existing immutable
pool mappings retain their recorded resources and require separate authenticated
registration to change. This action never changes pool maxima or starts runners.
Removing an entry from a mix does not delete its global profile.

Counts are desired configuration, not a measured or enforced capacity. Zero counts
are valid. CPU is whole vCPUs in millis; RAM and disk must be positive MiB. Summed
counts and resource products are overflow checked. No current fit, physical guest
partition qualification, or automatic scaling is asserted by saving a mix.

GET `/admin/api/v1/runner-mix/{host}` uses the existing read credential. POST
`/admin/api/v1/runner-mix` requires the management credential and same-origin
checks, with `{expected_sequence, mix}`. A successful receipt identifies the exact
saved revision and says `saved_profiles_and_desired_mix_not_mapped`.
