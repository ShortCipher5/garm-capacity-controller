import React, {useEffect, useId, useRef, useState} from "react";
import {ciAttemptKey, ciHealthExpiry, ciHealthLease, ciHealthLeaseRemaining, ciHistoryLease, ciJobKey, ciStepKey, qualifiedCIJobURL, validateCIHealthSnapshot} from "./ci-health-client.js";
import {ciHealthWorkflowCounts, ciWorkflowKey} from "./ci-health-counts.js";
import {observedActiveJobs} from "./ci-health-active-jobs.js";
import {longestObservedSteps} from "./ci-health-longest-steps.js";
import {focusCIJob, focusCIStep} from "./ci-health-step-navigation.js";
import {CIHealthHistory} from "./ci-health-history.jsx";
import {ciByteText, ciUsageCoverage, ciUsageReasonText, ciUsageStateLabel} from "./ci-health-usage.js";

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
function At({value, precise = false}) {
  const shown = precise ? value.slice(0, -1) : value.slice(0, 19);
  return <time dateTime={value}>{shown.replace("T", " ")} UTC</time>;
}
function StatusObserved({at}) {
  return <div><dt>Status observed</dt><dd>{at ? <At value={at} precise /> : <span className="muted">Unknown</span>}</dd></div>;
}
function Cutoff({label, deadline}) {
  const value = new Date(deadline).toISOString();
  return <p className="muted"><strong>{label}: </strong><time dateTime={value}>{value.replace("T", " ").replace("Z", " UTC")}</time></p>;
}

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
function ResourceUsage({usage}) {
  const measured = usage.measured;
  const covered = measured ? (measured.cpu ? measured.cpu.covered_ns : measured.ram && measured.ram.covered_ns ? measured.ram.covered_ns : "0") : "0";
  return <div>
    <dt>Resource usage</dt>
    <dd>
      <span className="pill">{ciUsageStateLabel(usage.state)}</span>
      {" "}
      <span className="muted">
        {usage.state === "verified" || usage.state === "partial"
          ? "Independently attributed to this exact attempt and job."
          : ciUsageReasonText(usage.reason)}
      </span>
      {measured && <dl className="ci-health-facts">
        <div><dt>Samples</dt><dd>{measured.samples} over {ciUsageCoverage(covered, measured.window_ns) ?? "unknown"} of the job window</dd></div>
        {measured.cpu && <div><dt>CPU</dt><dd>average {measured.cpu.average_millicpu} mCPU, interval peak {measured.cpu.peak_interval_millicpu} mCPU{measured.cpu.complete ? "" : " (partial coverage)"}</dd></div>}
        {measured.ram && <div><dt>RAM</dt><dd>time-weighted mean {measured.ram.mean_bytes !== undefined ? ciByteText(measured.ram.mean_bytes) : "unknown"}, sampled peak {measured.ram.sample_peak_bytes !== undefined ? ciByteText(measured.ram.sample_peak_bytes) : "unknown"}</dd></div>}
        <div><dt>Disk</dt><dd>{measured.disk.state === "verified"
          ? <>attributable usage: {["read_bytes", "write_bytes"].filter((key) => measured.disk[key]).map((key) => measured.disk[key] && `${key === "read_bytes" ? "read" : "written"} ${ciByteText(measured.disk[key])}`).filter(Boolean).join(", ") || "measured"}</>
          : ciUsageReasonText(measured.disk.reason)}</dd></div>
      </dl>}
    </dd>
  </div>;
}
function JobRow({attempt, job, jobId, stepId, openJob}) {
  const skip = job.status === "skipped";
  return (
    <li id={jobId(ciJobKey(attempt, job.id))} data-ci-job-row="" tabIndex={-1}>
      <div className="ci-health-row-head">
        <strong>Job <code>{job.id}</code></strong> <Phase status={job.status} />
        {job.job_link_handle && openJob && <button type="button" onClick={() => openJob(attempt, job)}>Open exact job on GitHub</button>}
      </div>
      <dl className="ci-health-facts">
        <StatusObserved at={job.status_observed_at} />
        <div><dt>Result</dt><dd><Result conclusion={job.conclusion} /></dd></div>
        {job.status === "queued"
          ? <div><dt>Waiting when observed</dt><dd><Timing span={job.waiting} /></dd></div>
          : <div><dt>Queue wait</dt><dd><Timing span={job.queue} skipped={skip} /></dd></div>}
        {(job.status === "running" || job.status === "completed") &&
          <div><dt>Job creation to start</dt><dd><Timing span={job.creation_to_start} /></dd></div>}
        {job.status === "running"
          ? <div><dt>Execution elapsed when observed</dt><dd><Timing span={job.elapsed} /></dd></div>
          : <div><dt>Execution</dt><dd><Timing span={job.execution} skipped={skip} /></dd></div>}
        {job.resource_usage && <ResourceUsage usage={job.resource_usage} />}
      </dl>
      {job.steps.length > 0
        ? <details data-ci-job-steps=""><summary>Numbered steps ({job.steps.length} observed)</summary><StepRows attempt={attempt} job={job} stepId={stepId} /></details>
        : <p className="muted">No step observations in this snapshot.</p>}
    </li>
  );
}
function AttemptRow({attempt: a, jobId, stepId, openJob}) {
  return (
    <details className="ci-health-attempt" data-ci-attempt="" open={activeAttempt(a) || undefined}>
      <summary><strong>Run <code>{a.run_id}</code>, attempt <code>{a.attempt}</code></strong> <Phase status={a.status} /></summary>
      <dl className="ci-health-facts">
        <div><dt>Repository ID</dt><dd><code>{a.repository_id}</code></dd></div>
        <div><dt>Workflow ID</dt><dd><code>{a.workflow_id}</code></dd></div>
        <div><dt>Source ID</dt><dd><code>{a.source}</code></dd></div>
        <StatusObserved at={a.status_observed_at} />
        <div><dt>Latest included read</dt><dd><At value={a.observed_at} precise /></dd></div>
        <div><dt>Result</dt><dd><Result conclusion={a.conclusion} /></dd></div>
        {a.status === "running"
          ? <div><dt>Attempt elapsed when observed</dt><dd><Timing span={a.elapsed} /></dd></div>
          : <div><dt>Attempt wall time</dt><dd><Timing span={a.wall} skipped={a.status === "skipped"} /></dd></div>}
      </dl>
      {a.jobs.length > 0
        ? <ul className="ci-health-jobs" aria-label="Observed jobs">{a.jobs.map((job) => <JobRow key={job.id} attempt={a} job={job} jobId={jobId} stepId={stepId} openJob={openJob} />)}</ul>
        : <p className="muted">No job observations in this snapshot.</p>}
    </details>
  );
}

function ActiveJobGroup({phase, group, jobId, viewJob}) {
  const title = phase === "queued" ? "Queued jobs" : "Running jobs";
  const heading = "ci-active-jobs-" + phase;
  const coverage = heading + "-coverage";
  return <section aria-labelledby={heading}>
    <h4 id={heading}>{title} ({group.observed} observed)</h4>
    {group.observed === 0 ? <p className="muted">None observed in this snapshot.</p> : <>
      <p className="muted" id={coverage}>
        {phase === "queued" ? "Wait" : "Elapsed"} timing available for {group.timed} of {group.observed} observed jobs
        {group.lowerBounds > 0 && ` (${group.lowerBounds} lower bound${group.lowerBounds === 1 ? "" : "s"})`}.
      </p>
      {group.shown.length < group.observed && <p className="muted">
        Showing {group.shown.length} of {group.observed} observed jobs. All observed jobs remain in the attempt details below.
      </p>}
      <div className="table-wrap">
        <table aria-labelledby={heading} aria-describedby={coverage}>
          <thead><tr>
            <th scope="col">Job ID</th><th scope="col">Run and attempt</th><th scope="col">Workflow scope</th>
            <th scope="col">Status observed</th>
            <th scope="col">{phase === "queued" ? "Waiting when observed" : "Execution elapsed when observed"}</th>
            <th scope="col">Details</th>
          </tr></thead>
          <tbody>{group.shown.map((row) => <tr key={row.key}>
            <th scope="row"><code>{row.job}</code></th>
            <td>Run <code>{row.run}</code>, attempt <code>{row.attempt}</code></td>
            <td className="ci-health-workflow-key">
              <span>Workflow <code>{row.workflow}</code></span>
              <span>Repository <code>{row.repository}</code></span>
              <span>Source <code>{row.source}</code></span>
            </td>
            <td>{row.statusObservedAt ? <At value={row.statusObservedAt} precise /> : <span className="muted">Unknown</span>}</td>
            <td><Timing span={row.timing} /></td>
            <td><button type="button" aria-controls={jobId(row.key)}
              aria-label={`View job ${row.job}, run ${row.run}, attempt ${row.attempt}, workflow ${row.workflow}, repository ${row.repository}, source ${row.source}`}
              onClick={() => viewJob(row.key)}>View job</button></td>
          </tr>)}</tbody>
        </table>
      </div>
    </>}
  </section>;
}

function ActiveJobs({attempts, jobId, viewJob}) {
  const {queued, running} = observedActiveJobs(attempts);
  return <section aria-labelledby="ci-active-jobs">
    <h3 id="ci-active-jobs">Active jobs in this snapshot</h3>
    <p className="muted">Jobs are grouped by their recorded status. This is not the live queue. Rows are ordered by identity, not by wait or runtime.</p>
    <ActiveJobGroup phase="queued" group={queued} jobId={jobId} viewJob={viewJob} />
    <ActiveJobGroup phase="running" group={running} jobId={jobId} viewJob={viewJob} />
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

function RetainedWorkflowCoverage({groups, history}) {
  // Both lists are already validated and bounded by the timing snapshot.
  const included = new Set(history.workflows.map(ciWorkflowKey));
  const omitted = groups.filter((group) => !included.has(ciWorkflowKey(group)));
  return <>
    <p className="muted">Retained history included for {included.size} of {groups.length} {groups.length === 1 ? "workflow" : "workflows"} in this timing snapshot.</p>
    {omitted.length > 0 && <>
      <p className="muted">Retained history not included for:</p>
      <ul>
        {omitted.map((group) => <li key={ciWorkflowKey(group)}>
          Workflow <code>{group.workflow_id}</code>, repository <code>{group.repository_id}</code>, source <code>{group.source}</code>
        </li>)}
      </ul>
    </>}
  </>;
}

export function CIHealthTiming({snapshot, now = Date.now(), canNavigate, showHistory = false, openJob}) {
  const prefix = useId();
  const root = useRef(null);
  const groups = ciHealthWorkflowCounts(snapshot, now);
  if (groups === null) return null;
  const attempts = snapshot.timing.attempts;
  const historyDeadline = snapshot.history ? Date.parse(snapshot.history.expires_at) : null;
  // The owner panel supplies the monotonic lease gate. Recheck the wall
  // cutoff here too, so a formerly true render flag cannot show late rows.
  const historyVisible = showHistory && historyDeadline !== null && historyDeadline > now;
  const active = attempts.filter(activeAttempt);
  const other = attempts.filter((a) => !activeAttempt(a));
  const jobId = (key) => prefix + "-ci-job-" + key;
  const viewJob = (key) => focusCIJob(root.current, jobId(key), canNavigate);
  const stepId = (key) => prefix + "-ci-step-" + key;
  const viewStep = (key) => focusCIStep(root.current, stepId(key), canNavigate);
  return (
    <div className="ci-health-timing" ref={root}>
      <p><strong>Snapshot observed:</strong> <At value={snapshot.observed_at} /></p>
      <Cutoff label="Timing display cutoff" deadline={ciHealthExpiry(snapshot)} />
      {historyVisible ? <Cutoff label="Retained history display cutoff" deadline={historyDeadline} />
        : <>
          {snapshot.history
            ? <p role="status" className="muted">Retained history has expired for this snapshot. Current timing remains visible.</p>
            : <p className="muted">No retained history was included in this snapshot.</p>}
          <p className="muted">Verified test reports are unavailable. Timing and job outcomes are not test report coverage.</p>
        </>}
      <p className="muted">
        {snapshot.coverage === "bounded_partial" ? "Partial coverage: not all requested observations were included. "
          : snapshot.coverage === "observed_subset" ? "Only selected observations are shown. "
          : "Coverage is unknown. "}
        This is a bounded observation, not the full GitHub queue or history. “At least” is a lower bound from a queue observation. Missing timing is unknown, not zero. Job creation to start does not measure queue wait.
      </p>
      {attempts.some((attempt) => attempt.jobs.length > 0) &&
        (attempts.some((a) => a.jobs.some((job) => job.resource_usage))
          ? <p className="muted">Per-job resource usage below is exact attempt/job attribution with explicit unavailable, stale and partial states. It is read-only advisory evidence, never a sizing or scaling input. Missing coverage is unknown, not zero.</p>
          : <p className="muted">Per-job CPU, memory, and disk usage are unavailable: no documented source proves the runner's original provider allocation. Timing alone cannot show resource use.</p>)}
      {attempts.length === 0 && <p>No attempts were included in this snapshot.</p>}
      {attempts.length > 0 && <>
        <ActiveJobs attempts={attempts} jobId={jobId} viewJob={viewJob} />
        <WorkflowCounts groups={groups} />
        {historyVisible && <>
          <RetainedWorkflowCoverage groups={groups} history={snapshot.history} />
          <CIHealthHistory history={snapshot.history} durationText={displayDuration} />
        </>}
        <LongestSteps attempts={attempts} stepId={stepId} viewStep={viewStep} />
        <h3>Active attempts at observation</h3>
        {active.length ? active.map((a) => <AttemptRow key={ciAttemptKey(a)} attempt={a} jobId={jobId} stepId={stepId} openJob={openJob} />) : <p>No active attempts in this snapshot.</p>}
        <h3>Other observed attempts</h3>
        {other.length ? other.map((a) => <AttemptRow key={ciAttemptKey(a)} attempt={a} jobId={jobId} stepId={stepId} openJob={openJob} />) : <p>No other attempts in this snapshot.</p>}
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
  const jobRequest = useRef(null);
  const owner = useRef(null);
  const [jobStatus, setJobStatus] = useState("");
  const refreshGeneration = useRef(0);
  const available = ciHealthNavigationAvailable(enabled, read);
  owner.current = available ? read : null;
  const current = view?.owner === read ? view : null;
  const snapshot = current?.snapshot;
  const lease = current?.lease;
  const historyLease = current?.historyLease;
  const state = current?.state ?? "idle";

  useEffect(() => () => {
    request.current?.abort();
    request.current = null;
    jobRequest.current?.abort();
    jobRequest.current = null;
  }, [available, read]);

  useEffect(() => {
    if (!snapshot) return;
    let timeout;
    const expire = () => {
      clearTimeout(timeout);
      const remaining = ciHealthLeaseRemaining(snapshot, lease);
      if (remaining < 1) {
        jobRequest.current?.abort();
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

  function currentView() {
    return owner.current === read && refreshGeneration.current === current?.generation &&
      ciHealthLeaseRemaining(snapshot, lease) > 0;
  }

  async function openJob(a, job) {
    if (!currentView() || !job.job_link_handle) return;
    jobRequest.current?.abort();
    const controller = new AbortController();
    jobRequest.current = controller;
    const active = () => jobRequest.current === controller && !controller.signal.aborted && currentView();
    const jobLink = {
      handle: job.job_link_handle, source: a.source, repository_id: a.repository_id,
      workflow_id: a.workflow_id, run_id: a.run_id, attempt: a.attempt, job_id: job.id,
    };
    setJobStatus("Resolving exact GitHub job…");
    try {
      const raw = await read({signal: controller.signal, jobLink});
      if (!active()) return;
      const href = qualifiedCIJobURL(raw, jobLink.run_id, jobLink.job_id);
      if (href) window.location.assign(href);
      else setJobStatus("GitHub job destination unavailable. Load a fresh timing snapshot.");
    } catch {
      if (active()) setJobStatus("GitHub job destination unavailable. Load a fresh timing snapshot.");
    } finally {
      if (jobRequest.current === controller) jobRequest.current = null;
    }
  }

  async function refresh() {
    if (!available) return;
    request.current?.abort();
    jobRequest.current?.abort();
    setJobStatus("");
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
      <p className="muted">Loading checks the latest available observation; it does not start a GitHub collection or automatically update the view. A fresh observation may not be available between collections.</p>
      <div role="status">
        {state === "loading" && "Reading CI Health…"}
        {state === "unavailable" && "CI Health timing is unavailable. Try again to read a new snapshot."}
        {state === "expired" && "The snapshot expired. Load a new snapshot to view timing."}
      </div>
      {snapshot && ciHealthLeaseRemaining(snapshot, lease) > 0 && jobStatus && <p role="status">{jobStatus}</p>}
      {snapshot && ciHealthLeaseRemaining(snapshot, lease) > 0 && <CIHealthTiming key={current.generation} snapshot={snapshot}
        showHistory={ciHealthLeaseRemaining(snapshot, historyLease) > 0}
        openJob={openJob} canNavigate={currentView} />}
    </section>
  );
}
