# Qualify the stock GARM v0.2.2 upgrade

This is a dashboard compatibility and stock migration qualification. It is
not evidence of production deployment, Incus authorization, or safe unattended
pool writes. Keep autoscaling and management writes disabled.

The pinned upgrade fixture starts v0.2.1 in an isolated Docker namespace,
initializes an ephemeral account, and proves the protected-file login path.
It stops only the disposable stock instance, takes a cold state copy, and
starts the pinned v0.2.2 image on that same fixture state. It rechecks the
stock/controller identity, login, bounded jobs read, bridge policy, and
dashboard. Finally it starts v0.2.1 using the pre-upgrade copy and verifies
login and identity. This is a rehearsal with synthetic Incus and empty stock
inventory; separate tests cover records after the first page, malformed page
metadata, and preserving v0.2.1 array behavior. V0.2.2 collection here means
ordinary webhook jobs, not scale-set jobs or a transactionally consistent
inventory across pages.

Before any real cutover:

1. Identify exact current image, entrypoint, CLI, data volume and database
   backend. Review the [upstream v0.2.2 upgrade notes](https://github.com/cloudbase/garm/releases/tag/v0.2.2):
   the server now uses `--config`, old CLIs cannot parse new job listings,
   and the shipped runner templates must be reconciled. Export and compare
   edited defaults and clones before any restore, which can overwrite edits.
2. Qualify migration and restore with an approved restorable copy of the
   production database and configuration in an isolated environment. Prevent
   clone access to real forges, runners, callbacks and live Incus. Preserve
   image digests and both old and new runtime configuration. Never start
   v0.2.1 on the migrated v0.2.2 database.
3. At the approved cutover, capture a fresh application-consistent backup
   before migration and preserve the original state for rollback. Keep the
   existing stock hostname, TLS and webhook routes. Confirm controller
   identity, service health, scoped pool and job inventory, and a known
   legitimate runner lifecycle before declaring the migration verified.
4. Supply the existing GARM login through the reader's owner-only password
   file and mount it read-only. The browser and gateway never receive GARM's
   admin credential. Validate password-file login and the separate operator
   access privately before exposing the dashboard; keep stock, reader and
   bridge raw APIs off the public route. If protected enrollment or live
   inventory is unavailable, do not report dashboard deployment complete.
5. If reverting, stop only the new components and stock candidate as
   appropriate, restore the last known compatible image, configuration and
   database, and re-verify stock before restarting observation. A rollback
   after accepting new writes or job events can lose post-backup state;
   handle that explicitly in the approved maintenance plan.
