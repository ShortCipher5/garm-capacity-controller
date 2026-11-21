import {ciJobKey} from "./ci-health-client.js";

const maxRowsPerPhase = 12;
const compareText = (a, b) => a < b ? -1 : a > b ? 1 : 0;
const compareDecimal = (a, b) => a.length - b.length || compareText(a, b);

// Only called with the existing validated, fresh snapshot. Group each
// included job by its own recorded phase.
export function observedActiveJobs(attempts) {
  const byPhase = {queued: [], running: []};
  for (const attempt of attempts) {
    for (const job of attempt.jobs) {
      if (job.status !== "queued" && job.status !== "running") continue;
      byPhase[job.status].push({
        key: ciJobKey(attempt, job.id),
        source: attempt.source,
        repository: attempt.repository_id,
        workflow: attempt.workflow_id,
        run: attempt.run_id,
        attempt: attempt.attempt,
        job: job.id,
        timing: job.status === "queued" ? job.waiting : job.elapsed,
      });
    }
  }

  const summarize = (rows) => ({
    observed: rows.length,
    // Order by exact identities, not bounds, duration or apparent queue age.
    shown: rows.sort((a, b) => compareText(a.source, b.source) ||
      compareDecimal(a.repository, b.repository) || compareDecimal(a.workflow, b.workflow) ||
      compareDecimal(a.run, b.run) || compareDecimal(a.attempt, b.attempt) ||
      compareDecimal(a.job, b.job)).slice(0, maxRowsPerPhase),
  });
  return {queued: summarize(byPhase.queued), running: summarize(byPhase.running)};
}
