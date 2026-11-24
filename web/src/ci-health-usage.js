// Per-job resource usage wire contract and display helpers. The shape
// validated here mirrors internal/cihealth's VerifiedJobUsageRecord.Wire
// projection exactly: omission reasons are explicit states, never zeros, and
// unavailable/stale records carry no measured numbers at all.

const usageStates = ["unavailable", "stale", "partial", "verified"];
const usageReasons = [
  "provider_origin_join_unsupported",
  "attribution_evidence_mismatch",
  "stale_evidence",
  "unbracketed_samples",
  "counter_discontinuity",
  "job_too_short_uncaptured",
  "overlapping_jobs",
  "disk_not_independently_measured",
  "disk_unsupported_by_sources",
  "collection_unavailable",
];
const maxInt64 = "9223372036854775807";
const maxUsageSamples = 4096;

const object = (v) => v !== null && typeof v === "object" && !Array.isArray(v);
const exactKeys = (value, required, optional = []) => object(value) &&
  required.every((key) => Object.hasOwn(value, key)) &&
  Object.keys(value).every((key) => required.includes(key) || optional.includes(key));
function decimal(value, allowZero = true) {
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
  return Number.isFinite(seconds) ? value : null;
}

function validMetric(value, kinds, windowNs) {
  if (!exactKeys(value, ["covered_ns", "complete"], kinds)) return false;
  if (!decimal(value.covered_ns) || typeof value.complete !== "boolean" ||
      !kinds.every((key) => Object.hasOwn(value, key) && decimal(value[key]))) return false;
  // Cross-field consistency: coverage can never exceed the window, and the
  // completeness flag must agree with exact full coverage. A malformed pair
  // hides holes or invents coverage, so the boundary refuses it.
  const covered = BigInt(value.covered_ns);
  if (covered > windowNs || value.complete !== (covered === windowNs)) return false;
  if (Object.hasOwn(value, "average_millicpu") && Object.hasOwn(value, "peak_interval_millicpu") &&
      BigInt(value.average_millicpu) > BigInt(value.peak_interval_millicpu)) return false;
  return true;
}

function validDisk(value, windowNs) {
  if (!exactKeys(value, ["state"], ["reason", "read_bytes", "write_bytes", "used_sample_peak_bytes", "covered_ns", "complete"])) return false;
  if (value.state !== "unavailable" && value.state !== "verified") return false;
  if (Object.hasOwn(value, "reason") && !usageReasons.includes(value.reason)) return false;
  if (value.state === "unavailable") {
    return !Object.hasOwn(value, "read_bytes") && !Object.hasOwn(value, "write_bytes") &&
      !Object.hasOwn(value, "used_sample_peak_bytes") && !Object.hasOwn(value, "covered_ns") &&
      !Object.hasOwn(value, "complete") && Object.hasOwn(value, "reason");
  }
  const quantities = ["read_bytes", "write_bytes", "used_sample_peak_bytes"].filter((key) => Object.hasOwn(value, key));
  if (quantities.length === 0 || !quantities.every((key) => decimal(value[key]))) return false;
  if (Object.hasOwn(value, "covered_ns")) {
    if (!decimal(value.covered_ns)) return false;
    const covered = BigInt(value.covered_ns);
    if (covered > windowNs) return false;
    if (Object.hasOwn(value, "complete") && typeof value.complete === "boolean" && value.complete !== (covered === windowNs)) return false;
  }
  return !Object.hasOwn(value, "complete") || typeof value.complete === "boolean";
}

export function validateCIJobResourceUsage(value) {
  if (!exactKeys(value, ["state", "as_of"], ["reason", "measured"])) return null;
  if (!usageStates.includes(value.state) || timestamp(value.as_of) === null) return null;
  if (Object.hasOwn(value, "reason")) {
    if ((value.state !== "unavailable" && value.state !== "stale") || !usageReasons.includes(value.reason)) return null;
  } else if (value.state === "unavailable" || value.state === "stale") {
    return null; // Omission without a reason hides why usage is absent.
  }
  const measured = value.measured;
  if (value.state === "unavailable" || value.state === "stale") {
    return Object.hasOwn(value, "measured") ? null : value;
  }
  if (!exactKeys(measured, ["samples", "window_ns", "disk"], ["cpu", "ram"])) return null;
  if (!Number.isInteger(measured.samples) || measured.samples < 1 || measured.samples > maxUsageSamples) return null;
  if (!decimal(measured.window_ns, false)) return null;
  const windowNs = BigInt(measured.window_ns);
  if (Object.hasOwn(measured, "cpu") && !validMetric(measured.cpu, ["average_millicpu", "peak_interval_millicpu"], windowNs)) return null;
  if (Object.hasOwn(measured, "ram")) {
    const ram = measured.ram;
    const optional = [];
    if (Object.hasOwn(ram, "covered_ns")) optional.push("covered_ns", "complete", "mean_bytes");
    if (Object.hasOwn(ram, "sample_peak_bytes")) optional.push("sample_peak_bytes", "gauge_samples");
    if (!exactKeys(ram, [], optional) || optional.length === 0) return null;
    if (Object.hasOwn(ram, "covered_ns")) {
      if (!decimal(ram.covered_ns) || typeof ram.complete !== "boolean" || !decimal(ram.mean_bytes)) return null;
      if (BigInt(ram.covered_ns) > windowNs || ram.complete !== (BigInt(ram.covered_ns) === windowNs)) return null;
    }
    if (Object.hasOwn(ram, "sample_peak_bytes") &&
        (!Number.isInteger(ram.gauge_samples) || ram.gauge_samples < 1 || ram.gauge_samples > measured.samples)) return null;
  }
  if (!validDisk(measured.disk, windowNs)) return null;
  return value;
}

const stateLabels = {unavailable: "Unavailable", stale: "Stale", partial: "Partial", verified: "Verified"};
const reasonTexts = {
  provider_origin_join_unsupported: "No documented source proves the runner's original provider allocation. A name, mutable provider label, timing, or separately authenticated reads cannot supply the join, so no resource numbers are shown.",
  attribution_evidence_mismatch: "The job assignment, stock row and instance evidence do not agree exactly, so nothing is attributed.",
  stale_evidence: "The observation expired before it could be verified. Load a fresh snapshot.",
  unbracketed_samples: "Resource samples do not positively bracket the exact job window; missing coverage is not zero.",
  counter_discontinuity: "Resource counters decreased, so a replacement or counter reset cannot be stitched into this job.",
  job_too_short_uncaptured: "The job ended before any sample covered it. Its usage is uncaptured, not zero.",
  overlapping_jobs: "Another job overlapped this runner window, so attribution to this job alone is ambiguous.",
  disk_not_independently_measured: "Disk usage is not independently measured for this job; host free disk is not job usage.",
};

export function ciUsageStateLabel(state) { return stateLabels[state] ?? "Unavailable"; }
export function ciUsageReasonText(reason) { return reasonTexts[reason] ?? "Verified usage is not available for this job."; }

export function ciUsageCoverage(coveredNanoseconds, windowNanoseconds) {
  const covered = BigInt(coveredNanoseconds), window = BigInt(windowNanoseconds);
  if (window <= 0n) return null;
  const percent = (100n * covered) / window;
  return covered >= window ? "100%" : percent + "%";
}

export function ciByteText(value) {
  const n = BigInt(value);
  if (n < 1024n) return n + " B";
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let scaled = n, unit = "B";
  for (const next of units) {
    if (scaled < 1024n) break;
    scaled /= 1024n;
    unit = next;
  }
  return scaled + " " + unit;
}
