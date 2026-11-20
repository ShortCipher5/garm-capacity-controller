import React, {useEffect, useRef, useState} from "react";
import {ciAttemptKey, ciHealthFresh, ciHealthLease, ciHealthLeaseRemaining, validateCIHealthSnapshot} from "./ci-health-client.js";

const statuses = {queued: "Queued", running: "Running", completed: "Completed", skipped: "Skipped", unknown: "Unknown"};
const activeAttempt = (a) => a.status === "queued" || a.status === "running";

export function ciHealthNavigationAvailable(enabled, read) {
  // This is render eligibility, not authentication. Only trusted startup
  // wiring supplies the marker; the same-origin route authorizes every read.
  return enabled === true && typeof read === "function";
}

function displayDuration(nanoseconds) {
  const n = BigInt(nanoseconds);
  const sec = n / 1_000_000_000n;
  const fraction = (n % 1_000_000_000n).toString().padStart(9, "0").replace(/0+$/, "");
  const seconds = (s) => s + (fraction ? "." + fraction : "") + " s";
  if (sec < 60n) {
    return seconds(sec);
  }
  const d = sec / 86400n;
  const h = (sec % 86400n) / 3600n;
  const m = (sec % 3600n) / 60n;
  const s = sec % 60n;
  return [d > 0n && d + " d", h > 0n && h + " h", m > 0n && m + " min", (s > 0n || fraction) && seconds(s)].filter(Boolean).join(" ");
}
function Timing({span, skipped = false}) {
  if (!span) return <span className="muted">{skipped ? "Not applicable (skipped)" : "Unknown"}</span>;
  return (
    <span title={span.nanoseconds + " nanoseconds"}>
      {span.basis === "observed_lower_bound" ? "At least " : ""}{displayDuration(span.nanoseconds)}
    </span>
  );
}
function Phase({status}) { return <span className="pill">{statuses[status]}</span>; }
function At({value}) { return <time dateTime={value}>{value.slice(0,19).replace("T", " ")} UTC</time>; }

function StepRows({steps}) {
  return (
    <div className="table-wrap">
      <table>
        <thead><tr><th scope="col">Step</th><th scope="col">Status</th><th scope="col">Execution</th><th scope="col">Elapsed when observed</th></tr></thead>
        <tbody>{steps.map((step) => (
          <tr key={step.number}>
            <th scope="row">Step {step.number}</th>
            <td><Phase status={step.status} /></td>
            <td><Timing span={step.execution} skipped={step.status === "skipped"} /></td>
            <td><Timing span={step.elapsed} skipped={step.status === "skipped"} /></td>
          </tr>
        ))}</tbody>
      </table>
    </div>
  );
}
function JobRow({job}) {
  const skip = job.status === "skipped";
  return (
    <li>
      <div className="ci-health-row-head">
        <strong>Job <code>{job.id}</code></strong> <Phase status={job.status} />
      </div>
      <dl className="ci-health-facts">
        {job.status === "queued"
          ? <div><dt>Waiting when observed</dt><dd><Timing span={job.waiting} /></dd></div>
          : <div><dt>Queue wait</dt><dd><Timing span={job.queue} skipped={skip} /></dd></div>}
        {job.status === "running"
          ? <div><dt>Execution elapsed when observed</dt><dd><Timing span={job.elapsed} /></dd></div>
          : <div><dt>Execution</dt><dd><Timing span={job.execution} skipped={skip} /></dd></div>}
      </dl>
      {job.steps.length > 0
        ? <details><summary>Numbered steps ({job.steps.length} observed)</summary><StepRows steps={job.steps} /></details>
        : <p className="muted">No step observations in this snapshot.</p>}
    </li>
  );
}
function AttemptRow({attempt: a}) {
  return (
    <details className="ci-health-attempt" open={activeAttempt(a) || undefined}>
      <summary><strong>Run <code>{a.run_id}</code>, attempt <code>{a.attempt}</code></strong> <Phase status={a.status} /></summary>
      <dl className="ci-health-facts">
        <div><dt>Repository ID</dt><dd><code>{a.repository_id}</code></dd></div>
        <div><dt>Workflow ID</dt><dd><code>{a.workflow_id}</code></dd></div>
        <div><dt>Source ID</dt><dd><code>{a.source}</code></dd></div>
        <div><dt>Last observed</dt><dd><At value={a.observed_at} /></dd></div>
        {a.status === "running"
          ? <div><dt>Attempt elapsed when observed</dt><dd><Timing span={a.elapsed} /></dd></div>
          : <div><dt>Attempt wall time</dt><dd><Timing span={a.wall} skipped={a.status === "skipped"} /></dd></div>}
      </dl>
      {a.jobs.length > 0
        ? <ul className="ci-health-jobs" aria-label="Observed jobs">{a.jobs.map((job) => <JobRow key={job.id} job={job} />)}</ul>
        : <p className="muted">No job observations in this snapshot.</p>}
    </details>
  );
}

export function CIHealthTiming({snapshot, now = Date.now()}) {
  try { validateCIHealthSnapshot(snapshot); } catch { return null; }
  if (!ciHealthFresh(snapshot, now)) return null;
  const attempts = snapshot.timing.attempts;
  const active = attempts.filter(activeAttempt);
  const other = attempts.filter((a) => !activeAttempt(a));
  return (
    <div className="ci-health-timing">
      <p><strong>Snapshot observed:</strong> <At value={snapshot.observed_at} /></p>
      <p className="muted">
        {snapshot.coverage === "bounded_partial" ? "Partial coverage: not all requested observations were included. "
          : snapshot.coverage === "observed_subset" ? "Only selected observations are shown. "
          : "Coverage is unknown. "}
        This is a bounded observation, not the full GitHub queue or history. “At least” is a lower bound from a queue observation. Missing timing is unknown, not zero.
      </p>
      {attempts.length === 0 && <p>No attempts were included in this snapshot.</p>}
      {attempts.length > 0 && <>
        <h3>Active at observation</h3>
        {active.length ? active.map((a) => <AttemptRow key={ciAttemptKey(a)} attempt={a} />) : <p>No active attempts in this snapshot.</p>}
        <h3>Other observed attempts</h3>
        {other.length ? other.map((a) => <AttemptRow key={ciAttemptKey(a)} attempt={a} />) : <p>No other attempts in this snapshot.</p>}
      </>}
    </div>
  );
}

// Only an explicit user click during the authorized dashboard session invokes
// the optional route. Timer and tab visibility handlers clear expired content;
// neither automatically fetches or updates observations.
export function CIHealthPanel({enabled, read}) {
  const [view, setView] = useState(null);
  const request = useRef(null);
  const available = ciHealthNavigationAvailable(enabled, read);
  const current = view?.owner === read ? view : null;
  const snapshot = current?.snapshot;
  const lease = current?.lease;
  const state = current?.state ?? "idle";

  useEffect(() => () => {
    request.current?.abort();
    request.current = null;
  }, [available, read]);

  useEffect(() => {
    if (!snapshot) return;
    let timeout;
    const expire = () => {
      clearTimeout(timeout);
      const remaining = ciHealthLeaseRemaining(snapshot, lease);
      if (remaining < 1) {
        setView((current) => current?.lease === lease ? {owner: current.owner, state: "expired"} : current);
      } else {
        timeout = setTimeout(expire, Math.floor(remaining));
      }
    };
    expire();
    document.addEventListener("visibilitychange", expire);
    return () => {
      clearTimeout(timeout);
      document.removeEventListener("visibilitychange", expire);
    };
  }, [snapshot, lease]);

  async function refresh() {
    if (!available) return;
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setView({owner: read, state: "loading"});
    try {
      const result = await read({signal: controller.signal});
      if (request.current !== controller || controller.signal.aborted) return;
      if (!result) {
        setView({owner: read, state: "unavailable"});
        return;
      }
      validateCIHealthSnapshot(result);
      const lease = ciHealthLease(result);
      if (!lease) {
        setView({owner: read, state: "unavailable"});
        return;
      }
      setView({owner: read, state: "observed", snapshot: result, lease});
    } catch {
      if (request.current === controller && !controller.signal.aborted) setView({owner: read, state: "unavailable"});
    } finally {
      if (request.current === controller) request.current = null;
    }
  }

  if (!available) return null;
  return (
    <section className="card ci-health" id="ci-health" aria-labelledby="ci-health-heading">
      <div className="section-heading">
        <h2 id="ci-health-heading">CI Health</h2>
        <button type="button" disabled={state === "loading"} onClick={refresh}>{snapshot ? "Refresh timing snapshot" : "Load timing snapshot"}</button>
      </div>
      <p className="muted">Read-only workflow, job and step timing. This view does not show logs, test results, runner utilization or sizing recommendations.</p>
      <div role="status">
        {state === "loading" && "Reading CI Health…"}
        {state === "unavailable" && "CI Health timing is unavailable. Try again to read a new snapshot."}
        {state === "expired" && "The snapshot expired. Load a new snapshot to view timing."}
      </div>
      {snapshot && ciHealthLeaseRemaining(snapshot, lease) > 0 && <CIHealthTiming snapshot={snapshot} />}
    </section>
  );
}
