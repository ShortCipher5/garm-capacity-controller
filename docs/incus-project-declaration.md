# Incus 7.0.2 project declaration comparison

The unwired comparator in `internal/incusapi/project_declaration.go` accepts
bounded fictional or externally supplied response bytes. It does not collect
them, establish their source, create an approval or enable any operation.

The supported wire objects are the exact v7.0.2 synchronous response bodies for
[one project](https://github.com/lxc/incus/blob/v7.0.2/cmd/incusd/api_project.go#L438-L516)
and
[one named profile scoped to that project](https://github.com/lxc/incus/blob/v7.0.2/cmd/incusd/profiles.go#L411-L510).
Profile GET can [fall back to the default project's
profiles](https://github.com/lxc/incus/blob/v7.0.2/internal/server/project/project.go#L301-L346)
when project profiles are disabled. Require the exact named non-default project,
explicit `features.profiles=true` and `restricted=true`, and the exact profile
name **and returned owning project**. These are necessary shape checks; they
do not prove that a project is dedicated to this workload.

This intentionally supports only a positive, finite container declaration:
explicit positive project instance, container, and whole-CPU limits; explicit
zero VM limit; absolute positive RAM and aggregate disk limits; one explicitly
sized `root` disk in the reviewed pool, no extra disks (other devices must be NICs),
and a profile with explicit whole CPU and hard memory limits. Profile sizes must
fit the corresponding project declarations. Count and CPU limits use canonical
decimal; byte limits accept positive integral bytes (optionally `B`) or
`KiB`, `MiB`, `GiB`, `TiB`, a conservative subset of the
[upstream unit parser](https://github.com/lxc/incus/blob/v7.0.2/shared/units/units.go#L19-L79).
Missing ceilings, percentages, pin sets, alternate spellings, pool-specific disk
limits and composite profile stacks are refused, not interpreted. The caps are
the declarations only; aggregate disk config is not a filesystem quota or proof
of physical disk headroom.

The two independent lowercase SHA-256 expectations commit to the exact
decoded contents. The input is the domain string, one NUL byte, and compact
Go `encoding/json.Marshal` output (sorted string-map keys, standard JSON
escaping). Envelope fields, formatting, descriptions and informational
`used_by` links are not part of the config commitments:

- `incus-project-config/v7.0.2`: object fields in order `name, config`.
- `incus-profile-config/v7.0.2`: object fields in order `project, name, config, devices`.

The maps include **every** config key and device field. A matching digest is not
a review if it was just calculated from those same observed bytes. Neither an
ETag nor a server-reported version establishes a deployment revision or the
effectively loaded executable. Incus [applies profiles in order and local
settings afterward](https://github.com/lxc/incus/blob/v7.0.2/internal/server/db/cluster/profiles.go#L117-L149).
Any future consumer must independently establish the approved build,
authenticated source, review provenance, freshness, and whether GARM actually
uses that single profile without relevant overrides. A matching declaration
cannot authorize a launch, a quota change, or a capacity release.

## Offline planned-mix report

`CompareIncus702ProjectPlan` accepts up to 32 planned pools, up to 32 separately
reviewed profile declarations under the same project and storage pool, and
up to 256 modeled runners. Each planned pool declares one profile. The same
profile can be used across pools, but all supplied profiles must be used and
each planned CPU/RAM/root-disk size must match the corresponding declaration.
It has no service caller, API, or automatic capture.

The existing single-host broker calculation checks the full proposed mix
against the project's aggregate caps and the independently supplied modeled
net host budget, separately. The static project ceiling is also compared
directly to the net host budget: a chosen mix fitting does not mean the project
ceiling would contain the host. Byte ceilings round down for fit and byte
consumption rounds up in the broker's MiB scale. Missing declarations, hash
or size drift, unreviewed profiles, unsupported mixtures, or a missing net
budget give an unknown report. This is not a current occupancy comparison; it
does not read, duplicate, or discount existing charges. Every result is
conditional on the supplied declarations and the assumed whole-project mix;
the report never establishes a live route or effective create overrides.

### Local invocation

Run `capacity-incus-project-plan < sanitized-capture.json` against a regular
local file. The command takes no flags or filenames, reads at most 3 MiB, and
does not contact GARM/Incus or use local service configuration or credentials.
The [fictional input example](../cmd/capacity-incus-project-plan/testdata/sanitized-capture.json)
shows the complete, closed `incus-project-plan-capture/v1` schema. Use separately
reviewed pins, not hashes derived merely from the captured response. Resource
objects use exact integer `cpu_millis`, `ram_mib`, `disk_mib`, and `runners`
fields. Input is local-only: do not include secrets, credentials, personal data,
or service addresses. Both metadata descriptions and used-by lists must be
empty; they are not committed fields. Do not redact or change committed
configuration/device values to force a match. If those values cannot safely
be supplied, leave the comparison unknown. This is not a capture or sanitization tool.

Standard output is one fixed `incus-project-plan-report/v1` JSON object and
never repeats input values, names, keys, digests, URLs, or raw parse errors.
Fields report the proposed mix against project caps and host budget separately,
plus the project ceiling against the host budget. `route_verified` and
`enforcement_verified` are always false. Exit codes:

- **0**: all three offline model comparisons fit. This supports independent
  review before a later authorized conformance test, not permission to run one.
- **1**: at least one modeled comparison exceeds its budget.
- **2**: unknown or unusable input, unsupported invocation, or output failure.

### Optional project-state cross-check for an intended empty baseline

To include an operator-supplied snapshot, use capture schema
`incus-project-plan-capture/v2`, with all v1 fields plus
`"project_state": {"project": "the-reviewed-project", "response": {...}}`.
The response must be the exact sanitized synchronous JSON body of
[the v7.0.2 project state GET](https://github.com/lxc/incus/blob/v7.0.2/cmd/incusd/api_project.go#L1477-L1503).
Use `Limit` and `Usage` exactly as serialized by the
[v7.0.2 Go response type](https://github.com/lxc/incus/blob/v7.0.2/shared/api/project.go#L81-L101).
This deliberately supports no pool-specific limit keys or alternate schema.
V1 captures and reports are unchanged. Both schemas require the modeled
aggregate `limits.disk` and explicitly sized profile root disk. This optional
consistency check cannot qualify a setup without those declarations, including
a soft disk-pressure-only setup that omits them.

A recognized v2 capture yields the v2 report with one additional redacted
field, `supplied_project_state`:

- `reported_allocations_zero`: the supplied counts and declared CPU/RAM/disk
  totals are zero and its instance/container/VM/CPU/RAM/disk caps exactly
  match the reviewed project declarations. Only this state can accompany a
  v2 success. Standalone networks are allowed; positive process allocations
  without instances are contradictory. Network and process caps are not
  compared to the declaration model, but neither allocation may exceed its
  own reported finite cap.
- `reported_allocations_nonzero`: the supplied allocatable counters are not all
  zero, including reported storage allocation without any instances. Exit 1.
- `conflicting`: supplied counts, allocations or caps contradict each other
  or the reviewed declarations. Exit 1.
- `unknown`: malformed, unsupported, mislabeled or unavailable data. Exit 2.

The response body has no project identity; the required matching project label
is supplied by the operator, not authenticated by this command. Incus reports
[database allocations](https://github.com/lxc/incus/blob/v7.0.2/internal/server/project/state.go#L14-L91),
not cgroups, in-flight operations, backing-filesystem usage, unused space or
current host headroom; some unset values are omitted from its sums. These
inputs do not prove the same daemon, snapshot freshness, complete coverage,
an empty physical project, route, verified enforcement, or safe runner
starts. Do not add these figures to a proposed full mix, treat them as free
capacity, or use them to settle a charge.

### Optional count/CPU/RAM model without sized disk declarations

Use the separate capture schema `incus-project-plan-capture/v3` only when
the modeled plan deliberately has no declared aggregate or per-profile root
disk size. Start with the
[fictional v3 capture](../cmd/capacity-incus-project-plan/testdata/sanitized-capture-without-disk.json).
This schema uses the v1 top-level keys but omits `disk_mib` from the closed
net-host-budget and pool-size objects. The reviewed project must omit
`limits.disk` and all `limits.disk.pool.*` keys, and the reviewed profile must
omit the root device's `size` field. V3 does not accept `project_state`.
All existing single-profile, root-device/pool, project ownership and exact
review-digest checks are retained; v1 and v2 remain unchanged.

The v3 report explicitly says `disk_assessment: "not_evaluated"`. Its three
fit results and exit status compare only runner count, whole CPU and hard
absolute RAM declarations. The complete planned mix is compared to both the
project declarations and the modeled net host envelope, with the declared
project ceiling compared separately to that envelope. There is no resource
assignment or interpretation of a missing disk value as a size or ceiling of
zero, and neither route nor enforcement is verified. This report does not
evaluate backing storage, quotas or live disk pressure. Check disk reserve,
pressure and any provider/storage defaults separately before qualification;
a modeled fit is not permission to start runners.
