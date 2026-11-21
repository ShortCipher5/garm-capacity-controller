import test from "node:test";
import assert from "node:assert/strict";
import {validateCIHealthSnapshot} from "./ci-health-client.js";
import {longestObservedSteps} from "./ci-health-longest-steps.js";

const at = "2026-01-01T00:00:00Z";
const exact = (nanoseconds) => ({basis: "observed_endpoints", nanoseconds});
const completed = (number, nanoseconds) => ({
  number, status: "completed", execution: exact(nanoseconds),
});
const attempt = (fields) => ({
  source: "fixture", repository_id: "9007199254740993", workflow_id: "17",
  run_id: "9223372036854775807", attempt: "1", observed_at: at, status: "running",
  ...fields,
});
const job = (steps) => ({id: "9007199254741001", status: "running", steps});
const validatedAttempts = (...attempts) => validateCIHealthSnapshot({
  state: "observed", observed_at: at, coverage: "bounded_partial", timing: {attempts},
}).timing.attempts;

test("ranks only completed exact durations, keeps known zero, and never merges across attempts or repos", () => {
  // Exercise adjacent integers that a JS Number would merge. This is an
  // exact-string wire boundary check, not a realistic long-job fixture.
  const slow = completed("10", "9007199254740993"), next = completed("2", "9007199254740992");
  const many = job([
    {number: "4", status: "running", elapsed: exact("9223372036854775807")},
    {number: "5", status: "skipped", conclusion: "other_terminal"},
    {number: "6", status: "completed", conclusion: "failure"},
    {...completed("3", "0"), conclusion: "failure"}, next, slow,
  ]);
  const first = attempt({jobs: [many]});
  const retry = attempt({attempt: "2", jobs: [job([{...next, conclusion: "success"}])]});
  const secondRepo = attempt({repository_id: "9007199254740994", run_id: "9223372036854775806", jobs: [job([next])]});
  const attempts = validatedAttempts(secondRepo, retry, first);
  const value = longestObservedSteps(attempts);
  assert.equal(value.observed, 8);
  assert.equal(value.measured, 5);
  assert.deepEqual(value.shown.map((row) => [row.repository, row.attempt, row.step, row.execution.nanoseconds, row.conclusion]), [
    ["9007199254740993", "1", "10", "9007199254740993", undefined],
    ["9007199254740993", "1", "2", "9007199254740992", undefined],
    ["9007199254740993", "2", "2", "9007199254740992", "success"],
    ["9007199254740994", "1", "2", "9007199254740992", undefined],
    ["9007199254740993", "1", "3", "0", "failure"],
  ]);
  assert.equal(new Set(value.shown.map((row) => row.key)).size, 5);
  assert.deepEqual(longestObservedSteps([...attempts].reverse()), value);
});

test("reports the full measured count but caps the list with numeric identity ties", () => {
  const steps = Array.from({length: 13}, (_, i) => completed(String(14 - i), "5"));
  const value = longestObservedSteps(validatedAttempts(attempt({jobs: [job(steps)]})));
  assert.equal(value.observed, 13);
  assert.equal(value.measured, 13);
  assert.deepEqual(value.shown.map((row) => row.step), Array.from({length: 12}, (_, i) => String(i + 2)));
  assert.deepEqual(longestObservedSteps(validatedAttempts(attempt({jobs: []}))), {
    observed: 0, measured: 0, shown: [],
  });
});
