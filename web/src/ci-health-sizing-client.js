import {ciHealthFresh, ciHealthLease, ciHealthLeaseRemaining, readCIHealthJSON} from "./ci-health-client.js";

export const ciSizingEndpoint = "/admin/api/v1/ci-health/sizing";
const maxSize = 1 << 20;

const outcomes = ["unreviewed", "increase_advised", "unchanged", "needs_manual_review", "needs_risk_or_outcome_review", "cohort_incomplete", "coverage_unavailable", "insufficient_history", "stale_history"];
const reasons = ["not_reviewed", "incomplete_cohort_coverage", "risk_evidence_present", "unsuccessful_jobs_present", "outcome_or_risk_unknown", "insufficient_primary_attempts", "insufficient_history_span", "missing_metric_coverage", "no_fresh_primary_completion", "change_exceeds_reviewed_bound", "within_oscillation_suppression_band"];
const warnings = ["sustained_cpu_pressure", "sustained_ram_pressure", "insufficient_spare_capacity"];
const rejectionReasons = ["incomparable_identity", "profile_changed", "stale", "duplicate"];

const object = (v) => v !== null && typeof v === "object" && !Array.isArray(v);
function exactKeys(value, required, optional = []) {
  return object(value) &&
    required.every((key) => Object.hasOwn(value, key)) &&
    Object.keys(value).every((key) => required.includes(key) || optional.includes(key));
}
function decimal(value, allowZero = false) {
  const maxInt64 = "9223372036854775807";
  return typeof value === "string" &&
    value.trim() === value &&
    ((allowZero && value === "0") || /^[1-9][0-9]{0,18}$/.test(value)) &&
    (value.length < 19 || value <= maxInt64);
}
function timestamp(value) {
  if (typeof value !== "string") return null;
  const m = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?Z$/.exec(value);
  if (!m || m[1].startsWith("0000") || m[0] !== value) return null;
  const seconds = Date.parse(m[1] + "Z");
  if (!Number.isFinite(seconds) || new Date(seconds).toISOString().slice(0, 19) !== m[1]) return null;
  const fraction = (m[2] ?? "").padEnd(9, "0");
  return { exact: m[1] + "." + fraction, milliseconds: seconds + Number(fraction.slice(0, 3)) };
}
const count = (n, max) => Number.isSafeInteger(n) && n >= 0 && n <= max;
const nullableDecimal = (value) => value === null || decimal(value, true);

// validateCISizingSnapshot accepts only the exact reviewed-advisory wire.
// Missing facts are null, never zero; unknown fields and relaxed enums reject
// the whole response so a degraded server cannot show a confident advice.
export function validateCISizingSnapshot(value) {
  const fail = () => { throw new Error("CI sizing response unavailable."); };
  if (!exactKeys(value, ["state", "sizing"], ["observed_at", "history"])) fail();
  if (value.state === "unavailable") {
    if (Object.hasOwn(value, "history") || Object.hasOwn(value, "observed_at") || value.sizing !== null) fail();
    return value;
  }
  const observed = timestamp(value.observed_at);
  if (value.state !== "observed" || !observed || !object(value.sizing)) fail();
  const s = value.sizing;
  if (!exactKeys(s, ["policy_version", "advisory_version", "outcome", "scope", "identity", "window", "freshness", "coverage", "samples", "observed", "installed", "cpu", "ram", "uncertainty", "host", "warnings", "disk", "rejected", "incomparable", "unverifiable", "foreign"]) ||
      typeof s.policy_version !== "string" || !s.policy_version ||
      typeof s.advisory_version !== "string" || !s.advisory_version ||
      !outcomes.includes(s.outcome) ||
      !["unknown", "partial", "complete"].includes(s.coverage) ||
      (s.uncertainty !== "" && s.uncertainty !== "sampled_peak_bound")) fail();
  if (!exactKeys(s.scope, ["source", "repository_id", "workflow_id"]) ||
      typeof s.scope.source !== "string" || !/^[A-Za-z0-9_-]{1,64}$/.test(s.scope.source) ||
      !decimal(s.scope.repository_id) || !decimal(s.scope.workflow_id)) fail();
  if (!exactKeys(s.identity, ["matrix", "profile", "revision"]) ||
      ![s.identity.matrix, s.identity.profile, s.identity.revision].every((label) => typeof label === "string" && label.length <= 128)) fail();
  const from = timestamp(s.window.from), asOf = timestamp(s.window.as_of);
  if (!exactKeys(s.window, ["from", "as_of", "latest_completion_max_age_ns"]) ||
      !from || !asOf || !(asOf.milliseconds > from.milliseconds) ||
      !decimal(s.window.latest_completion_max_age_ns)) fail();
  if (!exactKeys(s.freshness, ["latest_primary_completion", "age_ns", "fresh"]) ||
      typeof s.freshness.fresh !== "boolean" || !decimal(s.freshness.age_ns, true)) fail();
  const latest = s.freshness.latest_primary_completion === null ? null : timestamp(s.freshness.latest_primary_completion);
  if ((latest === null) !== (s.freshness.latest_primary_completion === null) ||
      (s.freshness.fresh && latest === null) ||
      (latest !== null && latest.milliseconds > asOf.milliseconds)) fail();
  if (!exactKeys(s.samples, ["eligible", "primary", "retried", "successful", "at_risk", "unknown", "unsuccessful", "throttled", "oom", "disk_full", "external_builder", "qualifying_cpu", "qualifying_ram"]) ||
      !Object.values(s.samples).every((n) => count(n, 1000)) ||
      s.samples.primary + s.samples.retried !== s.samples.eligible ||
      s.samples.qualifying_cpu > s.samples.successful || s.samples.qualifying_ram > s.samples.successful ||
      s.samples.at_risk + s.samples.unknown + s.samples.unsuccessful > s.samples.eligible) fail();
  if (!exactKeys(s.observed, ["cpu_peak_milli", "ram_peak_bytes", "cpu_saturated", "ram_saturated"]) ||
      !nullableDecimal(s.observed.cpu_peak_milli) || !nullableDecimal(s.observed.ram_peak_bytes) ||
      !count(s.observed.cpu_saturated, 1000) || !count(s.observed.ram_saturated, 1000) ||
      (s.uncertainty === "sampled_peak_bound") !== (s.observed.cpu_peak_milli !== null || s.observed.ram_peak_bytes !== null)) fail();
  if (!exactKeys(s.installed, ["cpu_milli", "ram_bytes"]) ||
      !decimal(s.installed.cpu_milli, true) || !decimal(s.installed.ram_bytes, true)) fail();
  for (const key of ["cpu", "ram"]) {
    const d = s[key];
    if (!exactKeys(d, ["state", "suggested", "reason"]) || !outcomes.includes(d.state)) fail();
    if (d.state === "increase_advised") {
      if (!decimal(d.suggested) || d.reason !== "") fail();
    } else {
      if (d.suggested !== null) fail();
      if (d.state === "unchanged") {
        if (d.reason !== "") fail();
      } else if (!reasons.includes(d.reason)) {
        fail();
      }
    }
  }
  if (!exactKeys(s.host, ["known", "host", "cpu_budget_milli", "cpu_allocated_milli", "cpu_headroom_milli", "ram_budget_bytes", "ram_allocated_bytes", "ram_headroom_bytes", "fit"]) ||
      typeof s.host.known !== "boolean" || typeof s.host.host !== "string" ||
      !["unknown", "fits", "does_not_fit"].includes(s.host.fit)) fail();
  const hostNumbers = [s.host.cpu_budget_milli, s.host.cpu_allocated_milli, s.host.cpu_headroom_milli, s.host.ram_budget_bytes, s.host.ram_allocated_bytes, s.host.ram_headroom_bytes];
  if (s.host.known) {
    if (s.host.host === "" || !hostNumbers.every((n) => decimal(n, true)) || s.host.fit === "unknown") fail();
  } else if (s.host.host !== "" || !hostNumbers.every((n) => n === null)) fail();
  if (!Array.isArray(s.warnings) || s.warnings.length > warnings.length ||
      !s.warnings.every((w) => warnings.includes(w)) || new Set(s.warnings).size !== s.warnings.length) fail();
  if (!exactKeys(s.disk, ["state", "peak_bytes", "samples", "qualified"]) ||
      !["unavailable", "observed"].includes(s.disk.state) ||
      (s.disk.state === "observed") !== (s.disk.peak_bytes !== null) ||
      (s.disk.peak_bytes !== null && !decimal(s.disk.peak_bytes, true)) ||
      !count(s.disk.samples, 1000) || !count(s.disk.qualified, 1000)) fail();
  if (!Array.isArray(s.rejected) || s.rejected.length > 1000) fail();
  for (const r of s.rejected) {
    if (!exactKeys(r, ["source", "repository_id", "workflow_id", "run_id", "attempt", "job_id", "reason"]) ||
        typeof r.source !== "string" || !/^[A-Za-z0-9_-]{1,64}$/.test(r.source) ||
        !decimal(r.repository_id) || !decimal(r.workflow_id) || !decimal(r.run_id) || !decimal(r.attempt) || !decimal(r.job_id) ||
        !rejectionReasons.includes(r.reason) ||
        // Rejection records must stay inside the authorized scope.
        r.source !== s.scope.source || r.repository_id !== s.scope.repository_id || r.workflow_id !== s.scope.workflow_id) fail();
  }
  if (!count(s.incomparable, 1000) || !count(s.unverifiable, 1000) || !count(s.foreign, 1000)) fail();
  if (Object.hasOwn(value, "history")) {
    const h = value.history, metrics = ["cpu_average", "cpu_peak", "ram_average", "ram_peak", "disk_peak"];
    if (!exactKeys(h, ["definition_sha256", "job_definition", "primary", "retries", "baseline"], ["current_run", "current_job", ...metrics]) ||
        typeof h.definition_sha256 !== "string" || h.definition_sha256.length !== 64 || !/^[0-9a-f]{64}$/.test(h.definition_sha256) ||
        typeof h.job_definition !== "string" || h.job_definition.trim() !== h.job_definition || !/^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$/.test(h.job_definition) ||
        ![h.primary,h.retries,h.baseline].every(n => count(n, 1000)) || h.primary + h.retries > 1000 ||
        h.primary < s.samples.primary || h.retries < s.samples.retried) fail();
    if (h.baseline === 0) {
      if (["current_run", "current_job", ...metrics].some(k => Object.hasOwn(h,k))) fail();
    } else if (h.baseline < 20 || h.baseline !== h.primary-1 || !decimal(h.current_run) || !decimal(h.current_job) ||
               s.coverage !== "complete" || !s.freshness.fresh || s.samples.successful !== h.primary) fail();
    const quantity = x => typeof x === "string" && x.trim() === x && /^(0|[1-9][0-9]{0,19})$/.test(x) && BigInt(x) <= 18446744073709551615n;
    for (const k of metrics) if (Object.hasOwn(h,k)) {
      const m = h[k];
      if (!exactKeys(m, ["current","baseline_median","direction"]) || !quantity(m.current) || !quantity(m.baseline_median) ||
          !["increase","decrease","within_baseline"].includes(m.direction)) fail();
      const cur = BigInt(m.current), base = BigInt(m.baseline_median);
      if (m.direction === "increase" && cur <= base || m.direction === "decrease" && cur >= base) fail();
    }
  }
  return value;
}

export async function readCISizingBody(response, signal) {
  return validateCISizingSnapshot(await readCIHealthJSON(response, signal, maxSize));
}

export function ciSizingFresh(value, now) {
  return ciHealthFresh(value, now);
}
export function ciSizingLease(value, wall, monotonic) {
  return ciHealthLease(value, wall, monotonic);
}
export function ciSizingLeaseRemaining(value, lease, wall, monotonic) {
  return ciHealthLeaseRemaining(value, lease, wall, monotonic);
}
