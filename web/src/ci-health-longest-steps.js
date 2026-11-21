import {ciStepKey} from "./ci-health-client.js";

const maxShownSteps = 12;
const lexical = (a, b) => a < b ? -1 : a > b ? 1 : 0;
// The validated snapshot supplies canonical nonnegative decimal strings.
const decimalOrder = (a, b) => a.length - b.length || lexical(a, b);

// Only called with the existing validated, fresh snapshot. These are
// separately measured observations, not stable step definitions or a trend.
export function longestObservedSteps(attempts) {
  let observed = 0;
  const measured = [];
  for (const attempt of attempts) {
    for (const job of attempt.jobs) {
      for (const step of job.steps) {
        observed++;
        if (step.status !== "completed" || step.execution?.basis !== "observed_endpoints") continue;
        measured.push({
          key: ciStepKey(attempt, job.id, step.number),
          source: attempt.source,
          repository: attempt.repository_id,
          workflow: attempt.workflow_id,
          run: attempt.run_id,
          attempt: attempt.attempt,
          job: job.id,
          step: step.number,
          execution: step.execution,
          conclusion: step.conclusion,
        });
      }
    }
  }
  measured.sort((a, b) => decimalOrder(b.execution.nanoseconds, a.execution.nanoseconds) ||
    lexical(a.source, b.source) || decimalOrder(a.repository, b.repository) ||
    decimalOrder(a.workflow, b.workflow) || decimalOrder(a.run, b.run) ||
    decimalOrder(a.attempt, b.attempt) || decimalOrder(a.job, b.job) ||
    decimalOrder(a.step, b.step));
  return {observed, measured: measured.length, shown: measured.slice(0, maxShownSteps)};
}
