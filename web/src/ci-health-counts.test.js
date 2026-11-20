import test from "node:test";
import assert from "node:assert/strict";
import {ciHealthWorkflowCounts, ciWorkflowKey} from "./ci-health-counts.js";

const at = "2026-01-01T00:00:00Z";
const now = Date.parse(at);
const scope = {source: "alpha", repository_id: "9007199254740993", workflow_id: "9007199254740995"};
const attempt = (run_id, attempt, status, conclusion, extras = {}) => ({
  ...scope, run_id, attempt, status, observed_at: at, jobs: [],
  ...(conclusion !== undefined ? {conclusion} : {}), ...extras,
});
const snapshot = (attempts, coverage = "bounded_partial") => ({
  state: "observed", coverage, observed_at: at, timing: {attempts},
});

test("counts only observed attempts, partitioned by exact workflow and initial vs retry", () => {
  const successesInJobs = [{id: "1", status: "completed", conclusion: "success", steps: []}];
  const shown = [
    attempt("1", "1", "completed", "success"),
    attempt("1", "2", "completed", "failure"),
    attempt("2", "1", "completed", "failure"),
    attempt("3", "1", "completed", "cancelled"),
    attempt("4", "1", "skipped"),
    attempt("5", "1", "queued"),
    attempt("6", "1", "running"),
    attempt("7", "1", "completed", undefined, {jobs: successesInJobs}),
    attempt("8", "9223372036854775807", "running"),
    attempt("9", "3", "completed", "timed_out"),
    attempt("10", "4", "skipped", "other_terminal"),
    attempt("11", "5", "unknown"),
  ];
  const [group] = ciHealthWorkflowCounts(snapshot(shown), now);
  assert.deepEqual(group, {
    ...scope,
    initial: {shown: 7, active: 2, success: 1, failure: 1, other: 1, unknown: 2},
    retries: {shown: 5, active: 1, success: 0, failure: 1, other: 2, unknown: 1},
  });
  for (const counts of [group.initial, group.retries]) {
    assert.equal(counts.shown, counts.active + counts.success + counts.failure + counts.other + counts.unknown);
  }
});

test("keeps exact IDs and case-sensitive source scopes without numeric coercion or inferred groups", () => {
  const rows = [
    attempt("1", "1", "completed", "success"),
    attempt("1", "1", "completed", "failure", {workflow_id: "9007199254740994"}),
    attempt("1", "2", "running", undefined, {repository_id: "9007199254740992"}),
    attempt("1", "1", "completed", undefined, {source: "Alpha"}),
  ];
  const result = ciHealthWorkflowCounts(snapshot(rows, "observed_subset"), now);
  assert.deepEqual(result.map(ciWorkflowKey), [
    "Alpha:9007199254740993:9007199254740995",
    "alpha:9007199254740992:9007199254740995",
    "alpha:9007199254740993:9007199254740994",
    "alpha:9007199254740993:9007199254740995",
  ]);
  assert.equal(result[0].initial.unknown, 1);
  assert.equal(result[1].initial.shown, 0);
  assert.equal(result[1].retries.active, 1);
  assert.equal(result[2].initial.failure, 1);
  assert.equal(result[3].initial.success, 1);
  assert.deepEqual(ciHealthWorkflowCounts(snapshot(rows.reverse(), "observed_subset"), now), result);
});

test("empty and omitted coverage are not runs or outcomes; stale and invalid data show nothing", () => {
  assert.deepEqual(ciHealthWorkflowCounts(snapshot([]), now), []);
  const raw = snapshot([attempt("1", "1", "completed")]);
  delete raw.coverage;
  assert.equal(ciHealthWorkflowCounts(raw, now)[0].initial.unknown, 1);
  assert.equal(ciHealthWorkflowCounts(raw, now - 1), null);
  assert.equal(ciHealthWorkflowCounts(raw, now + 120_000), null);
  assert.equal(ciHealthWorkflowCounts({state: "unavailable", timing: {attempts: []}}, now), null);
  assert.equal(ciHealthWorkflowCounts(snapshot([attempt("1", "1", "completed", "success<script>")]), now), null);
  assert.equal(ciHealthWorkflowCounts(snapshot([attempt("1", "1", "completed"), attempt("1", "1", "completed")]), now), null);
  assert.equal(ciHealthWorkflowCounts(snapshot([attempt("1", "1", "completed", undefined, {workflow_id: 700})]), now), null);
});
