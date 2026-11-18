# Explicit new native target configuration

This adds a genuine first-target configuration path. It does not create a dummy
preparation plan or activate a native owner. A saved SSH inventory endpoint is
paired, by explicit operator approval, with a separately pinned administrative
endpoint inside a deployment-approved CIDR/user/port/key scope. Equal or different
IP addresses do not prove that those endpoints identify the same physical host.

The deployment supplies frozen private templates, explicit image/storage/network
references and distinct owner TLS identity slots. The browser never supplies
private keys, private paths, arbitrary JSON sections, commands or binaries.

## Workflow

1. Read the sanitized catalog and saved inventory target digests.
2. Review an exact configuration, including both endpoint identities and required
   profile references.
3. Explicitly register it. A protected, locked journal durably consumes the
   preparation ID, target ID, host label, identity alias and both owner public-key
   fingerprints **before** writing fresh private inputs. An uncertain attempt
   remains consumed; no retry, adoption, identity reuse or cleanup is provided.
4. The registered target appears as pending configuration, not as a preparation
   Plan/View. Actual authenticated discovery and saved resource selection feed
   the existing fixed bundle builder.
5. Inspect the real completed bundle and explicitly approve its initial plan.
   The real bundle digest, saved selection and original registration are durably
   joined in the preparation journal before it enters the upload registry.
6. Upload/preparation is a separate existing explicitly approved action. Nothing
   here installs a unit, starts a daemon, releases charges or enables capacity.

A history refresh retains existing live services; it never closes or restarts an
in-flight preparation worker. Restart reconstructs only verified materialized
configuration and exact actual completed bundles. Unknown or corrupt history is
not treated as missing state.

## Scope and limits

- Dynamic configuration is deliberately separate from static target/recipe mode.
- At most eight consumed target slots per registry, matching the actual planning
  and assembly service bound. Unknown attempts also retain their slot.
- Catalog additions preserve exact earlier revisions through protected append-only
  history. Existing aliases, bytes, scopes and paths cannot change. Upgrade a
  legacy populated journal once with its unchanged original catalog before adding
  entries; addition-first migration refuses. See [catalog history](native-registration-catalog-history.md).
  The eight consumed-slot bound and permanent reservations remain unchanged;
  never discard journals to reuse keys or IDs.
- The template carries actual required storage roots/GUIDs, image identity and
  network specification. OS discovery fills no missing storage authority.
- Protected configuration is not physical enforcement proof. Runtime activation
  and physical release restrictions remain unchanged.
- Local disposable SSH fixtures exercise discovery. Real target preparation,
  target-kernel qualification and browser execution are not claimed by these
  source tests.

Registration history's `configuration_registered` is a durable historical fact,
not a statement that no bundle was subsequently built. A pending assembly target
is `registered_configuration_without_preparation_plan`: it may already have an
assembled bundle awaiting explicit first-plan approval.
