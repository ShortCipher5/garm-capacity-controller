import React from "react";
import {ciWorkflowKey} from "./ci-health-counts.js";

const levels = [["attempts", "Workflow attempts"], ["jobs", "Jobs (pooled)"], ["steps", "Steps (pooled)"]];
const isoTime = (value) => <time dateTime={value}>{value.slice(0, 19).replace("T", " ")} UTC</time>;

function FailureFraction({metric}) {
  const denominator = metric.success + metric.failure;
  return denominator ? <>{metric.failure}/{denominator} ({(100 * metric.failure / denominator).toFixed(1)}%)</>
    : <span className="muted">Unavailable</span>;
}

function DurationCoverage({metric}) {
  const timed = metric.duration?.samples ?? 0;
  if (!Object.hasOwn(metric, "completed")) return <>{timed} timed · <span className="muted">Completed count unknown</span></>;
  return <>{timed} of {metric.completed}</>;
}

function HistoryWindow({label, value, durationText}) {
  const initialFailures = value.initial.attempts.failure;
  return <div className="table-wrap">
    <table>
      <caption>{label}: {isoTime(value.from)} to {isoTime(value.to)}</caption>
      <thead><tr><th scope="col">Attempts</th><th scope="col">Timing level</th><th scope="col">Observed</th>
        <th scope="col">Success</th><th scope="col">Failure</th><th scope="col">Other/skipped</th>
        <th scope="col">Active</th><th scope="col">Unknown</th><th scope="col">Observed failure share</th>
        <th scope="col">Completed rows timed</th><th scope="col">Mean</th><th scope="col">P95</th></tr></thead>
      {[["Initial", value.initial], ["Retries", value.retries]].map(([group, bucket]) => <tbody key={group}>
        {levels.map(([field, name], i) => {
          const m = bucket[field], d = m.duration;
          return <tr key={field}>
            {i === 0 && <th scope="rowgroup" rowSpan={levels.length}>{group} ({bucket.distinct_runs} distinct runs)</th>}
            <th scope="row">{name}</th><td>{m.observed}</td><td>{m.success}</td><td>{m.failure}</td>
            <td>{m.other}</td><td>{m.active}</td><td>{m.unknown}</td>
            <td><FailureFraction metric={m} /></td><td><DurationCoverage metric={m} /></td>
            <td>{d ? durationText(d.mean_ns) : <span className="muted">Unavailable</span>}</td>
            <td>{d ? durationText(d.p95_ns) : <span className="muted">Unavailable</span>}</td>
          </tr>;
        })}
      </tbody>)}
    </table>
    {Object.hasOwn(value, "recovered_initial_failures") && <p>
      {label}: {initialFailures > 0 ?
        <>Successful retry observed for {value.recovered_initial_failures} of {initialFailures} observed initial failures.</> :
        <>No initial failures observed.</>}
    </p>}
  </div>;
}

export function CIHealthHistory({history, durationText}) {
  return <section aria-labelledby="ci-retained-history">
    <h3 id="ci-retained-history">Retained workflow timing and results</h3>
    <p><strong>Retained subset only.</strong> Uses the newest whole attempts, up to 64 attempts and 12,000 attempt, job and step records per reviewed workflow.
      Windows are based on each attempt’s last observation, not when a job ended.
      Earlier or unavailable observations are not counted. This is not complete workflow history.</p>
    <p className="muted">Failure share is failure divided by recorded success plus failure.
      Timing coverage counts only observed completed rows; skipped and active rows are not included.
      Mean and P95 use only rows with both endpoints. Jobs and steps are pooled, not grouped by equivalent definitions.
      A run can appear in both attempt types. Verified test reports are unavailable.</p>
    {history.workflows.some((w) => Object.hasOwn(w.current, "recovered_initial_failures")) &&
      <p className="muted">A successful retry must belong to the same run and be observed in either retained window.
        An unobserved retry does not establish the run’s eventual outcome.</p>}
    {history.workflows.map((w) => <details key={ciWorkflowKey(w)} className="ci-health-attempt">
      <summary>Workflow <code>{w.workflow_id}</code>, repository <code>{w.repository_id}</code>, source <code>{w.source}</code>
        {w.selection_limited && <> · Limited selection</>}</summary>
      {w.selection_limited && <p className="muted">Additional older retained attempts were not included because of the {w.selection_limited === "records"
        ? "12,000-record limit" : "64-attempt limit"}. No attempt was split.</p>}
      <HistoryWindow label="Most recent 7 days" value={w.current} durationText={durationText} />
      <HistoryWindow label="Previous 7 days" value={w.previous} durationText={durationText} />
    </details>)}
  </section>;
}
