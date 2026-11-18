# Explicit saved configuration assembly and preparation reload

This connects a fresh authenticated SSH selection to an actual local bundle and
then to the existing preparation workflow. It is not activation or physical
capacity authority. No assembly/reload action connects to an SSH target.

Deployment must enable native planning and native preparation, add fixed
`admin.native_preparation.assembly_recipes` keyed by the same target IDs, and opt
in with `--allow-native-bundle-assembly`. Each recipe is `nativeassembly.Recipe`:
its full independently approved Binding, fixed nativebundle Build configuration
(including protected export/private/expected-binding inputs), and protected
OutputParent. No paths or credentials are supplied by the browser. Initial
preparation targets still have a reviewed baseline bundle and journal.

`GET /admin/api/v1/native-assembly` requires the separate native-preparation token
and same origin, and returns `{targets: [nativeprovision.View]}` only for configured
assembly targets. The read exposes actual current registry plans, not fabricated
running status. The same authenticated route accepts strict POST bodies:

* build: action, target_id, selection_digest, confirm_build=true.
* inspect: action, target_id, selection_digest.
* reload: action, target_id, selection_digest, assembly_digest,
  current_review_digest, confirm_reload=true.

Build/inspect return `{target_id, assembly: nativeassembly.Report}`. Reload returns
that exact report plus `preparation: View` and state `registry_reloaded_not_prepared`.
The operator must separately load/review and authorize the normal SSH preparation
transfer afterward. Local assembly can read private deployment TLS/config bytes;
it does not send them to the target or return them to the browser.

An assembly intent is durable before output creation. Partial output is never
adopted or rebuilt by retry. If a response is lost, inspect the same selection.
Expired discovery cannot build a new bundle; historical inspection/restart may
validate already completed configuration without relabeling it fresh capacity.

Reload takes the same cross-process lock used by preparation execution. It refuses
any consumed preparation ID, including original intent, receipt or attempt files,
and requires the same exact pinned target, ID and journal. A bounded append-only
registry chain binds previous/new review digests and the exact selection digest.
It is fsynced before the in-memory service is replaced. Stale service instances
and old deployment bundles cannot execute after registration. A startup with the
assembly opt-in reopens the historical completed fixed recipe, verifies actual
bundle bytes against the durable current plan, then restores its service. Missing,
changed, malformed or partial metadata fails closed. It never restores an old
plan as a fallback or erases previous unknown work.

Registry reload is configuration-only. It does not install/start/enable a service,
modify global SSH, create runners, enroll capacity or refund reservations. The
native runtime boundary and physical-release gates remain unchanged.
