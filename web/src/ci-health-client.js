const maxInt64 = "9223372036854775807";
const maxSize = 1 << 20;
const phases = ["queued", "running", "completed", "skipped", "unknown"];
const terminalConclusions = ["success", "failure", "cancelled", "timed_out", "other_terminal"];
export const ciHealthEndpoint = "/admin/api/v1/ci-health";
const lifetime = 120_000; // Same display lifetime as the owner API.

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

export function validateCIHealthSnapshot(value) {
  const fail = () => { throw new Error("CI Health response unavailable."); };
  if (!exactKeys(value, ["state", "timing"], ["observed_at", "coverage"]) ||
      !exactKeys(value.timing, ["attempts"]) || !Array.isArray(value.timing.attempts) ||
      value.timing.attempts.length > 25) fail();
  if (value.state === "unavailable") {
    if (Object.hasOwn(value, "observed_at") || Object.hasOwn(value, "coverage") || value.timing.attempts.length !== 0) fail();
    return value;
  }
  const observed = timestamp(value.observed_at);
  if (value.state !== "observed" || !observed ||
      (Object.hasOwn(value, "coverage") && !["observed_subset", "bounded_partial"].includes(value.coverage))) fail();

  const attempts = new Set();
  let jobCount = 0;
  let stepCount = 0;
  for (const a of value.timing.attempts) {
    if (!exactKeys(a, ["source", "repository_id", "workflow_id", "run_id", "attempt", "observed_at", "status", "jobs"], ["wall", "elapsed", "conclusion"]) ||
        typeof a.source !== "string" || !/^[A-Za-z0-9_-]{1,64}$/.test(a.source) || a.source.trim() !== a.source ||
        ![a.repository_id, a.workflow_id, a.run_id, a.attempt].every((id) => decimal(id)) ||
        !phases.includes(a.status) || !validConclusion(a) || !validSpan(a, "wall", a.status === "completed") ||
        !validSpan(a, "elapsed", a.status === "running") || !Array.isArray(a.jobs) ||
        a.jobs.length > 250 - jobCount) fail();
    const at = timestamp(a.observed_at);
    const id = ciAttemptKey(a);
    if (!at || at.exact > observed.exact || attempts.has(id)) fail();
    attempts.add(id);
    jobCount += a.jobs.length;
    const jobs = new Set();
    for (const j of a.jobs) {
      if (!exactKeys(j, ["id", "status", "steps"], ["queue", "waiting", "execution", "elapsed", "conclusion"]) ||
          !decimal(j.id) || jobs.has(j.id) || !phases.includes(j.status) || !validConclusion(j) ||
          !validSpan(j, "queue", ["running", "completed"].includes(j.status), true) ||
          !validSpan(j, "waiting", j.status === "queued", true) ||
          !validSpan(j, "execution", j.status === "completed") ||
          !validSpan(j, "elapsed", j.status === "running") || !Array.isArray(j.steps) ||
          j.steps.length > 1000 - stepCount) fail();
      jobs.add(j.id);
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
  return value;
}

export function ciAttemptKey(a) {
  return [a.source, a.repository_id, a.workflow_id, a.run_id, a.attempt].join(":");
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

export async function readCIHealthBody(response, signal) {
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
      if (done) return validateCIHealthSnapshot(JSON.parse(text + decoder.decode()));
      length += value.byteLength;
      if (length > maxSize) break;
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
