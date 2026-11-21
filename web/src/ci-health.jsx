import React, {useEffect, useId, useRef, useState} from "react";
import {ciAttemptKey, ciHealthLease, ciHealthLeaseRemaining, ciHistoryLease, ciStepKey, validateCIHealthSnapshot} from "./ci-health-client.js";
import {ciHealthWorkflowCounts, ciWorkflowKey} from "./ci-health-counts.js";
import {observedActiveJobs} from "./ci-health-active-jobs.js";
import {longestObservedSteps} from "./ci-health-longest-steps.js";
import {focusCIStep} from "./ci-health-step-navigation.js";
import {CIHealthHistory} from "./ci-health-history.jsx";

const statuses = {queued: "Queued", running: "Running", completed: "Completed", skipped: "Skipped", unknown: "Unknown"};
const results = {success: "Success", failure: "Failure", cancelled: "Cancelled", timed_out: "Timed out", other_terminal: "Other terminal result"};
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
function Result({conclusion}) {
  return Object.hasOwn(results, conclusion) ? <span>{results[conclusion]}</span> : <span className="muted">Unknown</span>;
}
function At({value}) { return <time dateTime={value}>{value.slice(0,19).replace("T", " ")} UTC</time>; }

function StepRows({attempt, job, stepId}) {
  return (
    <div className="table-wrap">
      <table>
        <thead><tr><th scope="col">Step</th><th scope="col">Status</th><th scope="col">Result</th><th scope="col">Execution</th><th scope="col">Elapsed when observed</th></tr></thead>
        <tbody>{job.steps.map((step) => (
          <tr key={step.number} id={stepId(ciStepKey(attempt, job.id, step.number))} data-ci-step-row="" tabIndex={-1}>
            <th scope="row">Step {step.number}</th>
            <td><Phase status={step.status} /></td>
            <td><Result conclusion={step.conclusion} /></td>
            <td><Timing span={step.execution} skipped={step.status === "skipped"} /></td>
            <td><Timing span={step.elapsed} skipped={step.status === "skipped"} /></td>
          </tr>
        ))}</tbody>
      </table>
    </div>
  );
}
function JobRow({attempt, job, stepId}) {
  const skip = job.status === "skipped";
  return (
    <li>
      <div className="ci-health-row-head">
        <strong>Job <code>{job.id}</code></strong> <Phase status={job.status} />
      </div>
      <dl className="ci-health-facts">
        <div><dt>Result</dt><dd><Result conclusion={job.conclusion} /></dd></div>
        {job.status === "queued"
          ? <div><dt>Waiting when observed</dt><dd><Timing span={job.waiting} /></dd></div>
          : <div><dt>Queue wait</dt><dd><Timing span={job.queue} skipped={skip} /></dd></div>}
        {(job.status === "running" || job.status === "completed") &&
          <div><dt>Job creation to start</dt><dd><Timing span={job.creation_to_start} /></dd></div>}
        {job.status === "running"
          ? <div><dt>Execution elapsed when observed</dt><dd><Timing span={job.elapsed} /></dd></div>
          : <div><dt>Execution</dt><dd><Timing span={job.execution} skipped={skip} /></dd></div>}
      </dl>
      {job.steps.length > 0
        ? <details data-ci-job-steps=""><summary>Numbered steps ({job.steps.length} observed)</summary><StepRows attempt={attempt} job={job} stepId={stepId} /></details>
        : <p className="muted">No step observations in this snapshot.</p>}
    </li>
  );
}
function AttemptRow({attempt: a, stepId}) {
  return (
    <details className="ci-health-attempt" data-ci-attempt="" open={activeAttempt(a) || undefined}>
      <summary><strong>Run <code>{a.run_id}</code>, attempt <code>{a.attempt}</code></strong> <Phase status={a.status} /></summary>
      <dl className="ci-health-facts">
        <div><dt>Repository ID</dt><dd><code>{a.repository_id}</code></dd></div>
        <div><dt>Workflow ID</dt><dd><code>{a.workflow_id}</code></dd></div>
        <div><dt>Source ID</dt><dd><code>{a.source}</code></dd></div>
        <div><dt>Last observed</dt><dd><At value={a.observed_at} /></dd></div>
        <div><dt>Result</dt><dd><Result conclusion={a.conclusion} /></dd></div>
        {a.status === "running"
          ? <div><dt>Attempt elapsed when observed</dt><dd><Timing span={a.elapsed} /></dd></div>
          : <div><dt>Attempt wall time</dt><dd><Timing span={a.wall} skipped={a.status === "skipped"} /></dd></div>}
      </dl>
      {a.jobs.length > 0
        ? <ul className="ci-health-jobs" aria-label="Observed jobs">{a.jobs.map((job) => <JobRow key={job.id} attempt={a} job={job} stepId={stepId} />)}</ul>
        : <p className="muted">No job observations in this snapshot.</p>}
    </details>
  );
}

function ActiveJobGroup({phase, group}) {
  const title = phase === "queued" ? "Queued jobs" : "Running jobs";
  const heading = "ci-active-jobs-" + phase;
  return <section aria-labelledby={heading}>
    <h4 id={heading}>{title} ({group.observed} observed)</h4>
    {group.observed === 0 ? <p className="muted">None observed in this snapshot.</p> : <>
      {group.shown.length < group.observed && <p className="muted">
        Showing {group.shown.length} of {group.observed} observed jobs. All observed jobs remain in the attempt details below.
      </p>}
      <div className="table-wrap">
        <table aria-labelledby={heading}>
          <thead><tr>
            <th scope="col">Job ID</th><th scope="col">Run and attempt</th><th scope="col">Workflow scope</th>
            <th scope="col">{phase === "queued" ? "Waiting when observed" : "Execution elapsed when observed"}</th>
          </tr></thead>
          <tbody>{group.shown.map((row) => <tr key={row.key}>
            <th scope="row"><code>{row.job}</code></th>
            <td>Run <code>{row.run}</code>, attempt <code>{row.attempt}</code></td>
            <td className="ci-health-workflow-key">
              <span>Workflow <code>{row.workflow}</code></span>
              <span>Repository <code>{row.repository}</code></span>
              <span>Source <code>{row.source}</code></span>
            </td>
            <td><Timing span={row.timing} /></td>
          </tr>)}</tbody>
        </table>
      </div>
    </>}
  </section>;
}

function ActiveJobs({attempts}) {
  const {queued, running} = observedActiveJobs(attempts);
  return <section aria-labelledby="ci-active-jobs">
    <h3 id="ci-active-jobs">Active jobs in this snapshot</h3>
    <p className="muted">Jobs are grouped by their recorded status. This is not the live queue. Rows are ordered by identity, not by wait or runtime.</p>
    <ActiveJobGroup phase="queued" group={queued} />
    <ActiveJobGroup phase="running" group={running} />
  </section>;
}

function LongestSteps({attempts, stepId, viewStep}) {
  const steps = longestObservedSteps(attempts);
  return <section aria-labelledby="ci-longest-steps">
    <h3 id="ci-longest-steps">Longest measured steps in this snapshot</h3>
    <p className="muted" id="ci-longest-steps-key">
      {steps.measured} of {steps.observed} observed steps have a measured completed duration.
      {" "}This ranks individual observations only, not typical runtimes or regressions.
    </p>
    {steps.measured === 0 ? <p className="muted">No completed step durations were measured in this snapshot.</p> : <>
      {steps.shown.length < steps.measured && <p className="muted">
        Showing {steps.shown.length} of {steps.measured} measured steps. All observed steps remain in the attempt details below.
      </p>}
      <div className="table-wrap">
        <table aria-labelledby="ci-longest-steps" aria-describedby="ci-longest-steps-key">
          <thead><tr>
            <th scope="col">Job and step</th><th scope="col">Run and attempt</th><th scope="col">Workflow scope</th>
            <th scope="col">Execution</th><th scope="col">Result</th><th scope="col">Details</th>
          </tr></thead>
          <tbody>{steps.shown.map((row) => <tr key={row.key}>
            <th scope="row">Job <code>{row.job}</code>, step <code>{row.step}</code></th>
            <td>Run <code>{row.run}</code>, attempt <code>{row.attempt}</code></td>
            <td className="ci-health-workflow-key">
              <span>Workflow <code>{row.workflow}</code></span>
              <span>Repository <code>{row.repository}</code></span>
              <span>Source <code>{row.source}</code></span>
            </td>
            <td><Timing span={row.execution} /></td><td><Result conclusion={row.conclusion} /></td>
            <td><button type="button" aria-controls={stepId(row.key)}
              aria-label={`View step ${row.step}, job ${row.job}, run ${row.run}, attempt ${row.attempt}, workflow ${row.workflow}, repository ${row.repository}, source ${row.source}`}
              onClick={() => viewStep(row.key)}>View step</button></td>
          </tr>)}</tbody>
        </table>
      </div>
    </>}
  </section>;
}

function WorkflowCountCells({counts}) {
  return <>
    <td>{counts.shown}</td><td>{counts.active}</td><td>{counts.success}</td>
    <td>{counts.failure}</td><td>{counts.other}</td><td>{counts.unknown}</td>
  </>;
}

function WorkflowCounts({groups}) {
  return <section aria-labelledby="ci-workflow-counts">
    <h3 id="ci-workflow-counts">Observed attempt results by workflow</h3>
    <p><strong>Observed subset only:</strong> these counts cover the attempts shown below, not workflow totals or outcome rates.</p>
    <p className="muted" id="ci-workflow-counts-key">Active includes queued or running attempts. Other covers cancelled, timed out or another recorded terminal result. Unknown covers non-active attempts without a recorded result.</p>
    <div className="table-wrap">
      <table aria-labelledby="ci-workflow-counts" aria-describedby="ci-workflow-counts-key">
        <thead><tr>
          <th scope="col">Workflow identity</th><th scope="col">Attempt type</th><th scope="col">Shown</th>
          <th scope="col">Active</th><th scope="col">Success</th><th scope="col">Failure</th>
          <th scope="col">Other result</th><th scope="col">Unknown result</th>
        </tr></thead>
        {groups.map((row) => <tbody key={ciWorkflowKey(row)}>
          <tr>
            <th scope="rowgroup" rowSpan={2} className="ci-health-workflow-key">
              <span>Workflow <code>{row.workflow_id}</code></span>
              <span>Repository <code>{row.repository_id}</code></span>
              <span>Source <code>{row.source}</code></span>
            </th>
            <th scope="row">Initial</th>
            <WorkflowCountCells counts={row.initial} />
          </tr>
          <tr><th scope="row">Retries</th><WorkflowCountCells counts={row.retries} /></tr>
        </tbody>)}
      </table>
    </div>
  </section>;
}

export function CIHealthTiming({snapshot, now = Date.now(), canViewStep, showHistory = false}) {
  const prefix = useId();
  const root = useRef(null);
  const groups = ciHealthWorkflowCounts(snapshot, now);
  if (groups === null) return null;
  const attempts = snapshot.timing.attempts;
  const active = attempts.filter(activeAttempt);
  const other = attempts.filter((a) => !activeAttempt(a));
  const stepId = (key) => prefix + "-ci-step-" + key;
  const viewStep = (key) => focusCIStep(root.current, stepId(key), canViewStep);
  return (
    <div className="ci-health-timing" ref={root}>
      <p><strong>Snapshot observed:</strong> <At value={snapshot.observed_at} /></p>
      <p className="muted">
        {snapshot.coverage === "bounded_partial" ? "Partial coverage: not all requested observations were included. "
          : snapshot.coverage === "observed_subset" ? "Only selected observations are shown. "
          : "Coverage is unknown. "}
        This is a bounded observation, not the full GitHub queue or history. “At least” is a lower bound from a queue observation. Missing timing is unknown, not zero. Job creation to start does not measure queue wait.
      </p>
      {attempts.some((attempt) => attempt.jobs.length > 0) &&
        <p className="muted">Per-job CPU, memory, and disk usage are unavailable. Timing alone cannot show resource use.</p>}
      {attempts.length === 0 && <p>No attempts were included in this snapshot.</p>}
      {attempts.length > 0 && <>
        <ActiveJobs attempts={attempts} />
        <WorkflowCounts groups={groups} />
        {showHistory && snapshot.history && <CIHealthHistory history={snapshot.history} durationText={displayDuration} />}
        <LongestSteps attempts={attempts} stepId={stepId} viewStep={viewStep} />
        <h3>Active attempts at observation</h3>
        {active.length ? active.map((a) => <AttemptRow key={ciAttemptKey(a)} attempt={a} stepId={stepId} />) : <p>No active attempts in this snapshot.</p>}
        <h3>Other observed attempts</h3>
        {other.length ? other.map((a) => <AttemptRow key={ciAttemptKey(a)} attempt={a} stepId={stepId} />) : <p>No other attempts in this snapshot.</p>}
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
  const refreshGeneration = useRef(0);
  const available = ciHealthNavigationAvailable(enabled, read);
  const current = view?.owner === read ? view : null;
  const snapshot = current?.snapshot;
  const lease = current?.lease;
  const historyLease = current?.historyLease;
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

  useEffect(() => {
    if (!snapshot || !historyLease) return;
    let timeout;
    const expire = () => {
      clearTimeout(timeout);
      const remaining = ciHealthLeaseRemaining(snapshot, historyLease);
      if (remaining < 1) {
        setView((current) => current?.historyLease === historyLease ? {...current, historyLease: null} : current);
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
  }, [snapshot, historyLease]);

  async function refresh() {
    if (!available) return;
    request.current?.abort();
    const generation = ++refreshGeneration.current;
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
      const wall = Date.now(), monotonic = performance.now();
      const lease = ciHealthLease(result, wall, monotonic);
      if (!lease) {
        setView({owner: read, state: "unavailable"});
        return;
      }
      const historyLease = ciHistoryLease(result, wall, monotonic);
      setView({owner: read, state: "observed", snapshot: result, lease, historyLease, generation});
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
      <p className="muted">Read-only workflow, job and step timings and outcomes. This view does not show logs, individual test results, runner utilization or sizing recommendations.</p>
      <div role="status">
        {state === "loading" && "Reading CI Health…"}
        {state === "unavailable" && "CI Health timing is unavailable. Try again to read a new snapshot."}
        {state === "expired" && "The snapshot expired. Load a new snapshot to view timing."}
      </div>
      {snapshot && ciHealthLeaseRemaining(snapshot, lease) > 0 && <CIHealthTiming key={current.generation} snapshot={snapshot}
        showHistory={ciHealthLeaseRemaining(snapshot, historyLease) > 0}
        canViewStep={() => refreshGeneration.current === current.generation && ciHealthLeaseRemaining(snapshot, lease) > 0} />}
    </section>
  );
}
