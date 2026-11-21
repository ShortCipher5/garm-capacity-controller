import test from "node:test";
import assert from "node:assert/strict";
import {ciHealthLease, ciHealthLeaseRemaining, ciJobKey, ciStepKey} from "./ci-health-client.js";
import {focusCIJob, focusCIStep} from "./ci-health-step-navigation.js";

const exactAttempt = {
  source: "fixture", repository_id: "9007199254740993", workflow_id: "9007199254740995",
  run_id: "9223372036854775807", attempt: "2",
};
const exactJob = "9007199254740997";
const key = (attempt) => ciStepKey(attempt, exactJob, "7");

function fixture() {
  const events = [];
  const rows = new Map();
  const inside = new Set();
  const root = {
    isConnected: true,
    ownerDocument: {getElementById: (id) => rows.get(id)},
    contains: (node) => inside.has(node),
  };
  const makeStep = (id, {foreign = false, marked = true, nested = true} = {}) => {
    const detail = (group) => ({
      set open(value) {
        assert.equal(value, true);
        events.push([group, id]);
      },
    });
    const attempt = detail("attempt");
    const steps = detail("steps");
    steps.closest = (selector) => selector === "details[data-ci-attempt]" && nested ? attempt : null;
    const row = {
      matches: (selector) => selector === "tr[data-ci-step-row]" && marked,
      closest: (selector) => selector === "details[data-ci-job-steps]" ? steps : null,
      focus: () => events.push(["focus", id]),
    };
    rows.set(id, row);
    if (!foreign) for (const n of [attempt, steps, row]) inside.add(n);
  };
  return {root, makeStep, events};
}

test("opens and focuses only the exact full-identity row in this mounted snapshot", () => {
  const {root, makeStep, events} = fixture();
  const first = key({...exactAttempt, attempt: "1"});
  const retry = key(exactAttempt);
  assert.equal(retry, "fixture:9007199254740993:9007199254740995:9223372036854775807:2:9007199254740997:7");
  makeStep(first);
  makeStep(retry);
  assert.equal(focusCIStep(root, retry, () => true), true);
  assert.deepEqual(events, [["attempt", retry], ["steps", retry], ["focus", retry]]);
});

test("withdrawn, stale, foreign or unmounted targets do not disclose or focus a row", () => {
  const {root, makeStep, events} = fixture();
  const id = key(exactAttempt);
  makeStep(id);
  makeStep("foreign", {foreign: true});
  makeStep("wrong-tag", {marked: false});
  makeStep("wrong-hierarchy", {nested: false});
  const snapshot = {state: "observed", observed_at: "2026-01-01T00:00:00Z"};
  const originalWall = Date.parse(snapshot.observed_at);
  const lease = ciHealthLease(snapshot, originalWall, 1_000);
  // Even though wall time appears fresh, the original monotonic deadline passed.
  const expired = () => ciHealthLeaseRemaining(snapshot, lease, originalWall + 30_000, 121_000) > 0;
  for (const [target, active] of [[id, expired], [id, () => false], ["foreign", () => true],
    ["wrong-tag", () => true], ["wrong-hierarchy", () => true], ["missing", () => true]]) {
    assert.equal(focusCIStep(root, target, active), false);
  }
  assert.equal(focusCIStep(root, id), false);
  let checks = 0;
  assert.equal(focusCIStep(root, id, () => ++checks === 1), false);
  assert.equal(checks, 2);
  root.isConnected = false;
  assert.equal(focusCIStep(root, id, () => { assert.fail("detached snapshot"); }), false);
  assert.deepEqual(events, []);
});

test("jobs open and focus the exact attempt only while the mounted lease is current", () => {
  const first = ciJobKey({...exactAttempt, attempt: "1"}, exactJob);
  const retry = ciJobKey(exactAttempt, exactJob);
  assert.equal(retry, "fixture:9007199254740993:9007199254740995:9223372036854775807:2:9007199254740997");
  const events = [];
  const nodes = new Map();
  const inside = new Set();
  const root = {
    isConnected: true,
    ownerDocument: {getElementById: (id) => nodes.get(id)},
    contains: (node) => inside.has(node),
  };
  const makeJob = (id, {foreign = false, marked = true, nested = true} = {}) => {
    const attempt = {set open(value) { assert.equal(value, true); events.push(["attempt", id]); }};
    const row = {
      matches: (selector) => marked && selector === "li[data-ci-job-row]",
      closest: (selector) => nested && selector === "details[data-ci-attempt]" ? attempt : null,
      focus: () => events.push(["focus", id]),
    };
    nodes.set(id, row);
    if (!foreign) { inside.add(row); inside.add(attempt); }
  };
  makeJob(first);
  makeJob(retry);
  makeJob("foreign", {foreign: true});
  makeJob("wrong-tag", {marked: false});
  makeJob("wrong-hierarchy", {nested: false});
  const snapshot = {state: "observed", observed_at: "2026-01-01T00:00:00Z"};
  const wall = Date.parse(snapshot.observed_at);
  const lease = ciHealthLease(snapshot, wall, 1_000);
  const expired = () => ciHealthLeaseRemaining(snapshot, lease, wall + 30_000, 121_000) > 0;
  for (const id of ["foreign", "wrong-tag", "wrong-hierarchy", "missing"]) {
    assert.equal(focusCIJob(root, id, () => true), false);
  }
  assert.equal(focusCIJob(root, retry, expired), false);
  assert.equal(focusCIJob(root, retry), false);
  let checks = 0;
  assert.equal(focusCIJob(root, retry, () => ++checks === 1), false);
  assert.equal(checks, 2);
  assert.deepEqual(events, []);
  assert.equal(focusCIJob(root, retry, () => true), true);
  assert.deepEqual(events, [["attempt", retry], ["focus", retry]]);
});
