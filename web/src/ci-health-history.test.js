import test from "node:test";
import assert from "node:assert/strict";
import {mkdtemp, rm} from "node:fs/promises";
import {tmpdir} from "node:os";
import {join} from "node:path";
import React from "react";
import {renderToStaticMarkup} from "react-dom/server";
import {createServer} from "vite";
import {ciHealthLease, ciHealthLeaseRemaining, ciHistoryLease, validateCIHealthSnapshot} from "./ci-health-client.js";

const at = "2026-01-21T00:00:00.123456789Z";
const scope = {source: "fixture", repository_id: "9007199254740993", workflow_id: "9007199254740995"};
const none = () => ({observed: 0, success: 0, failure: 0, other: 0, active: 0, unknown: 0});
const zeroBucket = () => ({distinct_runs: 0, attempts: none(), jobs: none(), steps: none()});
function fixture() {
  const initial = zeroBucket();
  initial.distinct_runs = 2;
  initial.attempts = {...none(), observed: 2, success: 1, failure: 1};
  initial.jobs = {...none(), observed: 3, success: 1, failure: 1, unknown: 1,
    duration: {samples: 2, mean_ns: "1000000000", p95_ns: "2000000000"}};
  initial.steps = {...none(), observed: 1, success: 1, duration: {samples: 1, mean_ns: "0", p95_ns: "0"}};
  return {state: "observed", observed_at: at, coverage: "observed_subset",
    timing: {attempts: [{...scope, run_id: "9223372036854775807", attempt: "2", observed_at: at, status: "running", jobs: []}]},
    history: {coverage: "retained_subset", test_results: "unavailable", expires_at: "2026-01-21T00:01:00.123456789Z",
      workflows: [{...scope,
        previous: {from: "2026-01-07T00:00:00.123456789Z", to: "2026-01-14T00:00:00.123456789Z", initial: zeroBucket(), retries: zeroBucket()},
        current: {from: "2026-01-14T00:00:00.123456789Z", to: at, initial, retries: zeroBucket()},
      }]}};
}

test("retained history accepts only authorized scope, exact windows, bounded sample counts and no report claims", () => {
  const view = fixture();
  assert.equal(validateCIHealthSnapshot(view), view);
  const {history, ...timingOnly} = view;
  assert.equal(validateCIHealthSnapshot(timingOnly), timingOnly);
  for (const makeInvalid of [
    (v) => { v.coverage = "bounded_partial"; },
    (v) => { v.history.workflows[0].workflow_id = "9"; },
    (v) => { v.history.workflows.push(v.history.workflows[0]); },
    (v) => { v.history.workflows[0].current.to = "2026-01-21T00:00:00.123456788Z"; },
    (v) => { v.history.workflows[0].current.initial.jobs.success = 3; },
    (v) => { v.history.workflows[0].current.initial.jobs.duration.samples = 4; },
    (v) => { v.history.workflows[0].current.initial.jobs.duration.mean_ns = 0; },
    (v) => { v.history.workflows[0].current.initial.distinct_runs = 1; },
    (v) => { v.history.test_results = "verified"; },
    (v) => { v.history.coverage = "complete"; },
    (v) => { v.history.expires_at = "2026-01-21T00:03:00Z"; },
    (v) => { v.history.report_ids = [1]; },
  ]) {
    const bad = fixture();
    makeInvalid(bad);
    assert.throws(() => validateCIHealthSnapshot(bad), /CI Health response unavailable/);
  }
});

test("earlier history expiry does not renew on wall rollback or remove still-current timing", () => {
  const view = fixture();
  const wall = Date.parse(at), mono = 50;
  const history = ciHistoryLease(view, wall, mono);
  const timing = ciHealthLease(view, wall, mono);
  assert.ok(history && timing);
  assert.equal(ciHealthLeaseRemaining(view, history, wall + 10, mono + 59_999), 1);
  assert.equal(ciHealthLeaseRemaining(view, history, wall + 10, mono + 60_000), 0);
  assert.equal(ciHealthLeaseRemaining(view, timing, wall + 10, mono + 60_000), 60_000);
  assert.equal(ciHistoryLease(view, wall + 60_000, mono), null);
});

test("retained view labels coverage, actual denominator, unsupported tests and missing rather than zero", async () => {
  const cacheDir = await mkdtemp(join(tmpdir(), "garm-ci-history-render-"));
  let server;
  try {
    server = await createServer({cacheDir, server: {middlewareMode: true, hmr: false}, appType: "custom"});
    const {CIHealthTiming} = await server.ssrLoadModule("/src/ci-health.jsx");
    const snapshot = fixture();
    const render = (value, props = {}) => renderToStaticMarkup(React.createElement(CIHealthTiming, {
      snapshot: value, now: Date.parse(at) + 1, ...props,
    }));
    const markup = render(snapshot, {showHistory: true});
    assert.match(markup, /Retained workflow timing and results/);
    assert.match(markup, /not complete workflow history/);
    assert.match(markup, /Verified test reports are unavailable/);
    assert.match(markup, /1\/2 \(50\.0%\)/);
    assert.match(markup, /Jobs \(pooled\).*<td>2<\/td><td>1 s<\/td><td>2 s<\/td>/);
    assert.match(markup, /Steps \(pooled\).*<td>1<\/td><td>0 s<\/td><td>0 s<\/td>/);
    assert.match(markup, /<td>0<\/td><td><span class="muted">Unavailable<\/span><\/td>/);
    assert.doesNotMatch(render(snapshot), /Retained workflow timing and results/);
    assert.equal(render(snapshot, {showHistory: true, now: Date.parse(at) + 120_000}), "");
  } finally {
    try { await server?.close(); } finally { await rm(cacheDir, {recursive: true, force: true}); }
  }
});
