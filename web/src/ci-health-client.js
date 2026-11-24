const maxInt64 = "9223372036854775807";
const maxSize = 1 << 20;
const phases = ["queued", "running", "completed", "skipped", "unknown"];
const terminalConclusions = ["success", "failure", "cancelled", "timed_out", "other_terminal"];
export const ciHealthEndpoint = "/admin/api/v1/ci-health";
export const ciJobHandleHeader = "X-GARM-CI-Job-Handle";
export const ciRunHandleHeader = "X-GARM-CI-Run-Handle";
export const validCIJobHandle = (handle) => typeof handle === "string" &&
  /^[A-Za-z0-9_-]{42}[AEIMQUYcgkosw048]$/.test(handle);
export const validCIRunHandle = validCIJobHandle;

export function validateCIJobDestination(value, selection) {
  const fields = ["source", "repository_id", "workflow_id", "run_id", "attempt", "job_id"];
  if (!exactKeys(value, ["url", ...fields]) || !selection || !validCIJobHandle(selection.handle) ||
      !fields.every(key => Object.hasOwn(selection, key) && value[key] === selection[key]) ||
      typeof value.source !== "string" || !/^[A-Za-z0-9_-]{1,64}$/.test(value.source) ||
      !fields.slice(1).every(key => decimal(value[key])) || typeof value.url !== "string") return null;
  return qualifiedCIJobURL(value.url, value.run_id, value.job_id);
}

export function validateCIRunDestination(value, selection) {
  const fields = ["source", "repository_id", "workflow_id", "run_id", "attempt"];
  if (!exactKeys(value, ["url", ...fields]) || !selection || !validCIRunHandle(selection.handle) ||
      !fields.every(key => Object.hasOwn(selection, key) && value[key] === selection[key]) ||
      typeof value.source !== "string" || !/^[A-Za-z0-9_-]{1,64}$/.test(value.source) ||
      !fields.slice(1).every(key => decimal(value[key])) || typeof value.url !== "string") return null;
  return qualifiedCIRunURL(value.url, value.run_id);
}

export function qualifiedCIJobURL(url, run, job) {
  if (typeof url !== "string" || !decimal(run) || !decimal(job)) return null;
  const match = /^https:\/\/github\.com\/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}\/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}\/actions\/runs\/([1-9][0-9]{0,18})\/job\/([1-9][0-9]{0,18})$/.exec(url);
  return match && match[0] === url && match[1] === run && match[2] === job ? url : null;
}

// The logical run destination is shared by every retry; it cannot identify
// an exact attempt or job and never points outside the canonical host.
export function qualifiedCIRunURL(url, run) {
  if (typeof url !== "string" || !decimal(run)) return null;
  const match = /^https:\/\/github\.com\/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}\/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}\/actions\/runs\/([1-9][0-9]{0,18})$/.exec(url);
  return match && match[0] === url && match[1] === run ? url : null;
}
const lifetime = 120_000; // Same display lifetime as the owner API.
const trendWeek = 7 * 24 * 60 * 60 * 1000;

const object = (v) => v !== null && typeof v === "object" && !Array.isArray(v);
function exactKeys(value, required, optional = []) {
  return object(value) &&
    required.every((key) => Object.hasOwn(value, key)) &&
    Object.keys(value).every((key) => required.includes(key) || optional.includes(key));
}
function decimal(value, allowZero = false) {
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
  if (!Number.isFinite(seconds) || new Date(seconds).toISOString().slice(0,19) !== m[1]) return null;
  const fraction = (m[2] ?? "").padEnd(9, "0");
  return {exact: m[1] + "." + fraction, milliseconds: seconds + Number(fraction.slice(0,3))};
}

function validStatusObservation(row, through) {
  if (!Object.hasOwn(row, "status_observed_at")) return true;
  const status = timestamp(row.status_observed_at);
  return status !== null && status.exact <= through.exact;
}

function validSpan(row, key, permitted, lowerBound = false) {
  if (!Object.hasOwn(row, key)) return true;
  const span = row[key];
  return permitted && exactKeys(span, ["basis", "nanoseconds"]) &&
    (span.basis === "observed_endpoints" || lowerBound && span.basis === "observed_lower_bound") &&
    decimal(span.nanoseconds, true);
}

function validConclusion(row) {
  if (!Object.hasOwn(row, "conclusion")) return true;
  return (row.status === "completed" && terminalConclusions.includes(row.conclusion)) ||
    (row.status === "skipped" && row.conclusion === "other_terminal");
}

const count = (n, max) => Number.isInteger(n) && n >= 0 && n <= max;
function validRetainedMetric(metric, max) {
  const fields = ["observed", "success", "failure", "other", "active", "unknown"];
  if (!exactKeys(metric, fields, ["completed", "duration"]) || !fields.every((key) => count(metric[key], max)) ||
      metric.observed !== fields.slice(1).reduce((n, key) => n + metric[key], 0)) return false;
  const maxCompleted = metric.observed - metric.active;
  const completed = Object.hasOwn(metric, "completed") ? metric.completed : undefined;
  if (Object.hasOwn(metric, "completed") &&
      (!count(completed, maxCompleted) || completed < metric.success + metric.failure)) return false;
  if (!Object.hasOwn(metric, "duration")) return true;
  const d = metric.duration;
  return exactKeys(d, ["samples", "mean_ns", "p95_ns"]) && count(d.samples, completed ?? maxCompleted) &&
    d.samples > 0 && decimal(d.mean_ns, true) && decimal(d.p95_ns, true);
}

function validRetainedHistory(history, observed, scopes) {
  if (!exactKeys(history, ["expires_at", "coverage", "test_results", "workflows"]) ||
      history.coverage !== "retained_subset" || history.test_results !== "unavailable" ||
      !Array.isArray(history.workflows) || !history.workflows.length || history.workflows.length > scopes.size) return false;
  const expiry = timestamp(history.expires_at);
  if (!expiry || expiry.exact <= observed.exact || expiry.milliseconds > observed.milliseconds + lifetime) return false;
  const seen = new Set();
  for (const w of history.workflows) {
    if (!exactKeys(w, ["source", "repository_id", "workflow_id", "previous", "current"], ["selection_limited"]) ||
        typeof w.source !== "string" || !decimal(w.repository_id) || !decimal(w.workflow_id) ||
        (Object.hasOwn(w, "selection_limited") && !["attempts", "records"].includes(w.selection_limited))) return false;
    const key = [w.source, w.repository_id, w.workflow_id].join(":");
    if (seen.has(key) || !scopes.has(key)) return false;
    seen.add(key);
    let retainedAttempts = 0;
    let retainedRecords = 0;
    let recoveryWindows = 0;
    let recovered = 0;
    let possibleRecovered = 0;
    let end = null;
    for (const window of [w.previous, w.current]) {
      if (!exactKeys(window, ["from", "to", "initial", "retries"], ["recovered_initial_failures"])) return false;
      const from = timestamp(window.from), to = timestamp(window.to);
      if (!from || !to || to.milliseconds - from.milliseconds !== trendWeek ||
          from.exact.slice(20) !== to.exact.slice(20) || (end && from.exact !== end.exact)) return false;
      end = to;
      for (const [bucket, initial] of [[window.initial, true], [window.retries, false]]) {
        if (!exactKeys(bucket, ["distinct_runs", "attempts", "jobs", "steps"]) ||
            !validRetainedMetric(bucket.attempts, 64) || !validRetainedMetric(bucket.jobs, 12000) ||
            !validRetainedMetric(bucket.steps, 12000) || !count(bucket.distinct_runs, bucket.attempts.observed) ||
            (initial && bucket.distinct_runs !== bucket.attempts.observed) ||
            (!bucket.distinct_runs && (bucket.attempts.observed || bucket.jobs.observed || bucket.steps.observed)) ||
            (!bucket.jobs.observed && bucket.steps.observed)) return false;
        retainedAttempts += bucket.attempts.observed;
        retainedRecords += bucket.attempts.observed + bucket.jobs.observed + bucket.steps.observed;
      }
      if (Object.hasOwn(window, "recovered_initial_failures")) {
        if (!count(window.recovered_initial_failures, window.initial.attempts.failure)) return false;
        recoveryWindows++;
        recovered += window.recovered_initial_failures;
      }
      possibleRecovered += Math.min(window.retries.attempts.success, window.retries.distinct_runs);
    }
    if (end.exact !== observed.exact || retainedAttempts > 64 || retainedRecords > 12000 ||
        (recoveryWindows !== 0 && recoveryWindows !== 2) || recovered > possibleRecovered) return false;
  }
  return true;
}

export function validateCIHealthSnapshot(value) {
  const fail = () => { throw new Error("CI Health response unavailable."); };
  if (!exactKeys(value, ["state", "timing"], ["observed_at", "coverage", "history"]) ||
      !exactKeys(value.timing, ["attempts"]) || !Array.isArray(value.timing.attempts) ||
      value.timing.attempts.length > 25) fail();
  if (value.state === "unavailable") {
    if (Object.hasOwn(value, "observed_at") || Object.hasOwn(value, "coverage") || Object.hasOwn(value, "history") || value.timing.attempts.length !== 0) fail();
    return value;
  }
  const observed = timestamp(value.observed_at);
  if (value.state !== "observed" || !observed ||
      (Object.hasOwn(value, "coverage") && !["observed_subset", "bounded_partial"].includes(value.coverage))) fail();

  const attempts = new Set();
  const scopes = new Set();
  let jobCount = 0;
  let stepCount = 0;
  const handles = new Set();
  for (const a of value.timing.attempts) {
    if (!exactKeys(a, ["source", "repository_id", "workflow_id", "run_id", "attempt", "observed_at", "status", "jobs"], ["wall", "elapsed", "conclusion", "status_observed_at", "run_link_handle"]) ||
        typeof a.source !== "string" || !/^[A-Za-z0-9_-]{1,64}$/.test(a.source) || a.source.trim() !== a.source ||
        ![a.repository_id, a.workflow_id, a.run_id, a.attempt].every((id) => decimal(id)) ||
        (Object.hasOwn(a, "run_link_handle") && (!validCIRunHandle(a.run_link_handle) || handles.has(a.run_link_handle))) ||
        !phases.includes(a.status) || !validConclusion(a) || !validSpan(a, "wall", a.status === "completed") ||
        !validSpan(a, "elapsed", a.status === "running") || !Array.isArray(a.jobs) ||
        a.jobs.length > 250 - jobCount) fail();
    const at = timestamp(a.observed_at);
    const id = ciAttemptKey(a);
    if (!at || at.exact > observed.exact || !validStatusObservation(a, at) || attempts.has(id)) fail();
    attempts.add(id);
    if (a.run_link_handle) handles.add(a.run_link_handle);
    scopes.add([a.source, a.repository_id, a.workflow_id].join(":"));
    jobCount += a.jobs.length;
    const jobs = new Set();
    for (const j of a.jobs) {
      if (!exactKeys(j, ["id", "status", "steps"], ["queue", "waiting", "creation_to_start", "execution", "elapsed", "conclusion", "job_link_handle", "status_observed_at"]) ||
          !validStatusObservation(j, at) ||
          (Object.hasOwn(j, "job_link_handle") && (!validCIJobHandle(j.job_link_handle) || handles.has(j.job_link_handle))) ||
          !decimal(j.id) || jobs.has(j.id) || !phases.includes(j.status) || !validConclusion(j) ||
          !validSpan(j, "queue", ["running", "completed"].includes(j.status), true) ||
          !validSpan(j, "waiting", j.status === "queued", true) ||
          !validSpan(j, "creation_to_start", ["running", "completed"].includes(j.status)) ||
          !validSpan(j, "execution", j.status === "completed") ||
          !validSpan(j, "elapsed", j.status === "running") || !Array.isArray(j.steps) ||
          j.steps.length > 1000 - stepCount) fail();
      jobs.add(j.id);
      if (j.job_link_handle) handles.add(j.job_link_handle);
      stepCount += j.steps.length;
      const steps = new Set();
      for (const s of j.steps) {
        if (!exactKeys(s, ["number", "status"], ["execution", "elapsed", "conclusion"]) ||
            !decimal(s.number) || steps.has(s.number) || !phases.includes(s.status) || !validConclusion(s) ||
            !validSpan(s, "execution", s.status === "completed") ||
            !validSpan(s, "elapsed", s.status === "running")) fail();
        steps.add(s.number);
      }
    }
  }
  if (Object.hasOwn(value, "history") &&
      (value.coverage !== "observed_subset" || !validRetainedHistory(value.history, observed, scopes))) fail();
  return value;
}

export function ciAttemptKey(a) {
  return [a.source, a.repository_id, a.workflow_id, a.run_id, a.attempt].join(":");
}

export function ciJobKey(attempt, jobID) {
  return ciAttemptKey(attempt) + ":" + jobID;
}

export function ciStepKey(attempt, jobID, stepNumber) {
  return ciJobKey(attempt, jobID) + ":" + stepNumber;
}

export function ciHealthExpiry(value) {
  return value?.state === "observed" ? (timestamp(value.observed_at)?.milliseconds ?? -Infinity) + lifetime : -Infinity;
}
export function ciHealthFresh(value, now) {
  const at = value?.state === "observed" ? timestamp(value.observed_at) : null;
  return at !== null && observedAtNotFuture(at, now) && now < at.milliseconds + lifetime;
}
function observedAtNotFuture(at, now) {
  return Number.isFinite(now) && at.milliseconds <= now;
}

// Anchor display lifetime once, to the earlier of the server wall deadline
// and the same remaining interval measured by the browser monotonic clock.
// A backwards wall-clock adjustment cannot extend an accepted lease.
export function ciHealthLease(value, wall = Date.now(), monotonic = performance.now()) {
  if (!ciHealthFresh(value, wall) || !Number.isFinite(monotonic) || monotonic < 0) return null;
  const wallDeadline = ciHealthExpiry(value);
  return Object.freeze({wallDeadline, accepted: monotonic, monotonicDeadline: monotonic + (wallDeadline - wall)});
}

export function ciHealthLeaseRemaining(value, lease, wall = Date.now(), monotonic = performance.now()) {
  if (!lease || !ciHealthFresh(value, wall) || !Number.isFinite(monotonic) || monotonic < lease.accepted) return 0;
  return Math.max(0, Math.min(lease.wallDeadline - wall, lease.monotonicDeadline - monotonic));
}

export function ciHistoryLease(value, wall = Date.now(), monotonic = performance.now()) {
  const expires = timestamp(value?.history?.expires_at);
  const timing = ciHealthLease(value, wall, monotonic);
  if (!expires || !timing || expires.milliseconds <= wall || expires.milliseconds > timing.wallDeadline) return null;
  const wallDeadline = expires.milliseconds;
  return Object.freeze({wallDeadline, accepted: monotonic, monotonicDeadline: monotonic + (wallDeadline - wall)});
}

export async function readCIHealthBody(response, signal) {
  return validateCIHealthSnapshot(await readCIHealthJSON(response, signal, maxSize));
}

export async function readCIJobDestinationBody(response, signal, selection) {
  return validateCIJobDestination(await readCIHealthJSON(response, signal, 1024), selection);
}

export async function readCIRunDestinationBody(response, signal, selection) {
  return validateCIRunDestination(await readCIHealthJSON(response, signal, 1024), selection);
}

export async function readCIHealthJSON(response, signal, limit) {
  if (!/^application\/json(?:\s*;|$)/i.test(response.headers.get("Content-Type") ?? "") || !response.body) {
    throw new Error("CI Health response unavailable.");
  }
  const reader = response.body.getReader();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  const cancel = () => { void reader.cancel().catch(() => {}); };
  signal.addEventListener("abort", cancel, {once: true});
  let length = 0, text = "";
  try {
    while (!signal.aborted) {
      const {done, value} = await reader.read();
      if (signal.aborted) break;
      if (done) return JSON.parse(text + decoder.decode());
      length += value.byteLength;
      if (length > limit) break;
      text += decoder.decode(value, { stream: true });
    }
    throw new Error("CI Health response unavailable.");
  } finally {
    // Bounded read on oversized/invalid input; cancellation never starts a request.
    signal.removeEventListener("abort", cancel);
    cancel();
    reader.releaseLock();
  }
}
