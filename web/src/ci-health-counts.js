import {ciHealthFresh, validateCIHealthSnapshot} from "./ci-health-client.js";

const emptyCounts = () => ({shown: 0, active: 0, success: 0, failure: 0, other: 0, unknown: 0});
const lexical = (a, b) => a < b ? -1 : a > b ? 1 : 0;
// Decimal IDs are already canonical positive int64 strings; retain precision.
const decimalOrder = (a, b) => a.length - b.length || lexical(a, b);

export function ciWorkflowKey(row) {
  return [row.source, row.repository_id, row.workflow_id].join(":");
}

// Counts describe this exact fresh, validated observation only. Each shown
// attempt is counted once; reruns are not new workflow runs. No results are
// inferred from timings, jobs, or the presence of a terminal phase.
export function ciHealthWorkflowCounts(snapshot, now) {
  try { validateCIHealthSnapshot(snapshot); } catch { return null; }
  if (!ciHealthFresh(snapshot, now)) return null;
  const grouped = new Map();
  for (const row of snapshot.timing.attempts) {
    const key = ciWorkflowKey(row);
    if (!grouped.has(key)) {
      grouped.set(key, {
        source: row.source, repository_id: row.repository_id, workflow_id: row.workflow_id,
        initial: emptyCounts(), retries: emptyCounts(),
      });
    }
    const counts = grouped.get(key)[row.attempt === "1" ? "initial" : "retries"];
    counts.shown++;
    if (row.status === "queued" || row.status === "running") {
      counts.active++;
      continue;
    }
    switch (row.conclusion) {
      case "success": counts.success++; break;
      case "failure": counts.failure++; break;
      case "cancelled":
      case "timed_out":
      case "other_terminal": counts.other++; break;
      default: counts.unknown++;
    }
  }
  return [...grouped.values()].sort((a, b) => lexical(a.source, b.source) ||
    decimalOrder(a.repository_id, b.repository_id) || decimalOrder(a.workflow_id, b.workflow_id));
}
