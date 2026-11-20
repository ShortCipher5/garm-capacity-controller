# Read-only observed versus planned capacity

The existing repository policy panel can explicitly request a fresh preview.
GET /admin/api/v1/allocator-plan requires the existing admin read credential.
A configured real allocator source is required; missing source, stale or changed
observations, missing enrolled mapping and ledger disagreement return unavailable,
not zero demand or zero runners. The blocked runtime writer can still perform this
read; no runtime authority guard is removed.

The concrete DemandClient obtains scoped stock transport observations. Its opaque
source identity must match all immutable pool mappings. One canonical ledger
planning transaction supplies saved policy, current identities and retained
charges. The same enrolledCycle planner used by the grant cycle computes an
advisory plan without invoking expiry, maintenance, grant issuance or settings
writes. A second read rejects changed ledger sequence/policy before responding.
Source identity and freshness are rechecked. These are deliberately not described
as a transaction across GARM and SQLite: stock collection reads are non-atomic,
and neither missing webhooks nor later remote changes are covered.

The bounded projection contains capture interval, expiry, actual source/demand
digests and ledger/policy revisions; per-pool stock idle/busy/creating/other,
canonical runner charges, saved cap/warm target and planned additional runners.
Provisional idle/grant coverage is not job assignment; in-flight deferrals are
not fulfilled jobs. Missing stock supply is unknown, never an invented running
count. A failed refresh removes current figures; expiry and policy changes mark
retained figures historical. The preview also compares its ledger sequence with
the displayed saved ledger snapshot, even when the policy revision has not
changed. A newer saved snapshot supersedes the retained preview; a preview on a
later sequence is separately captured and must not be compared against the older
saved figures. Missing metadata leaves the comparison unavailable. Matching
revisions and sequences establish only a comparison of those captures, not live
state. Draft edits do not alter saved-plan results.

No pool reductions or desired final physical total are inferred: this is the
actual conservative allocator's additional-capacity plan, not a second scheduler.
This view does not authorize adoption, native execution, release or GARM writes.

When the optional stock maximum preview is enabled, the same panel displays
its status and bounded one-step proposals from the authenticated response.
Absent preview metadata adds no panel. Malformed, independently expired or
incomparable input does not replace ordinary advice or show proposal rows.
This display has no apply control and cannot authorize settings or runner changes.
