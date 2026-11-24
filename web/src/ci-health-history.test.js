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
const none = () => ({observed: 0, success: 0, failure: 0, other: 0, active: 0, unknown: 0, completed: 0});
const zeroBucket = () => ({distinct_runs: 0, attempts: none(), jobs: none(), steps: none()});
function fixture() {
  const initial = zeroBucket();
  initial.distinct_runs = 2;
  initial.attempts = {...none(), observed: 2, success: 1, failure: 1, completed: 2};
  initial.jobs = {...none(), observed: 3, success: 1, failure: 1, unknown: 1, completed: 3,
    duration: {samples: 2, mean_ns: "1000000000", p95_ns: "2000000000"}};
  initial.steps = {...none(), observed: 1, success: 1, completed: 1, duration: {samples: 1, mean_ns: "0", p95_ns: "0"}};
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
    (v) => { v.history.workflows[0].current.initial.jobs.completed = 1; },
    (v) => { v.history.workflows[0].current.initial.jobs.completed = 4; },
    (v) => { v.history.workflows[0].current.initial.jobs.completed = "3"; },
    (v) => { v.history.workflows[0].current.initial.jobs.completed = null; },
    (v) => { const jobs = v.history.workflows[0].current.initial.jobs; jobs.active = 1; jobs.unknown = 0; },
    (v) => { const jobs = v.history.workflows[0].current.initial.jobs; jobs.completed = 2; jobs.duration.samples = 3; },
    (v) => { v.history.workflows[0].current.initial.jobs.duration.mean_ns = 0; },
    (v) => { v.history.workflows[0].current.initial.distinct_runs = 1; },
    (v) => { v.history.workflows[0].selection_limited = "unknown"; },
    (v) => { v.history.workflows[0].selection_limited = null; },
    (v) => { const bucket = v.history.workflows[0].current.initial; bucket.steps = {...none(), observed: 11996, unknown: 11996}; },
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

test("retained history never accepts steps without an observed parent job", () => {
  const view = fixture();
  const bucket = view.history.workflows[0].current.initial;
  bucket.jobs = none();
  assert.throws(() => validateCIHealthSnapshot(view), /CI Health response unavailable/);
  // An observed attempt can have no job or step observations at all.
  bucket.steps = none();
  assert.equal(validateCIHealthSnapshot(view), view);
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

function retryRecoveryFixture() {
  const view = fixture();
  const {previous, current} = view.history.workflows[0];
  // The first-attempt failure is in the current window. The successful retry
  // was observed in the other window, before re-observing that older attempt.
  previous.retries.distinct_runs = 1;
  previous.retries.attempts = {...none(), observed: 1, success: 1, completed: 1};
  previous.recovered_initial_failures = 0;
  current.recovered_initial_failures = 1;
  return view;
}

test("retained retry recovery is optional for legacy windows and strictly bounded by both kinds of evidence", () => {
  const view = retryRecoveryFixture();
  assert.equal(validateCIHealthSnapshot(view), view);
  for (const change of [
    ({current}) => { current.recovered_initial_failures = -1; },
    ({current}) => { current.recovered_initial_failures = 2; },
    ({current}) => { current.recovered_initial_failures = "1"; },
    ({current}) => { delete current.recovered_initial_failures; },
    ({previous}) => { previous.recovered_initial_failures = 1; },
    ({previous}) => {
      previous.retries.attempts.success = 0;
      previous.retries.attempts.unknown = 1;
    },
    ({previous}) => {
      previous.initial = {distinct_runs: 1, attempts: {...none(), observed: 1, failure: 1, completed: 1}, jobs: none(), steps: none()};
      previous.recovered_initial_failures = 1;
    },
  ]) {
    const bad = retryRecoveryFixture();
    change(bad.history.workflows[0]);
    assert.throws(() => validateCIHealthSnapshot(bad), /CI Health response unavailable/);
  }
});

test("retained view labels coverage, actual denominator, unsupported tests and missing rather than zero", async () => {
  const cacheDir = await mkdtemp(join(tmpdir(), "garm-ci-history-render-"));
  let server;
  try {
    // This test only loads SSR modules. Do not start the client dependency
    // optimizer, which can race removal of the temporary cache at teardown.
    server = await createServer({
      cacheDir, optimizeDeps: {noDiscovery: true, include: []},
      server: {middlewareMode: true, hmr: false}, appType: "custom",
    });
    const {CIHealthTiming} = await server.ssrLoadModule("/src/ci-health.jsx");
    const snapshot = fixture();
    const render = (value, props = {}) => renderToStaticMarkup(React.createElement(CIHealthTiming, {
      snapshot: value, now: Date.parse(at) + 1, ...props,
    }));
    const markup = render(snapshot, {showHistory: true});
    assert.match(markup, /Retained workflow timing and results/);
    assert.match(markup, /Timing display cutoff: <\/strong><time dateTime="2026-01-21T00:02:00\.123Z">/);
    assert.match(markup, /Retained history display cutoff: <\/strong><time dateTime="2026-01-21T00:01:00\.123Z">/);
    const assertOneReportNotice = (html) => assert.equal((html.match(/Verified test reports are unavailable/g) ?? []).length, 1);
    const retainedSection = markup.match(/<section aria-labelledby="ci-retained-history">[\s\S]*?<\/section>/)?.[0];
    assert.ok(retainedSection, "retained history section should be rendered");
    assertOneReportNotice(retainedSection);
    assertOneReportNotice(markup);
    assert.match(markup, /Retained history included for 1 of 1 workflow in this timing snapshot/);
    assert.doesNotMatch(markup, /Retained history not included for:/);
    const partial = fixture();
    const first = partial.timing.attempts[0];
    partial.timing.attempts.push(
      {...first, attempt: "1"},
      {...first, source: "other_source"},
      {...first, repository_id: "9007199254740994"},
    );
    const partialMarkup = render(partial, {showHistory: true});
    assert.match(partialMarkup, /Retained history included for 1 of 3 workflows in this timing snapshot/);
    const omitted = partialMarkup.match(/Retained history not included for:<\/p><ul>(.*?)<\/ul>/)?.[1];
    assert.ok(omitted);
    assert.match(omitted, /Workflow <code>9007199254740995<\/code>, repository <code>9007199254740993<\/code>, source <code>other_source<\/code>/);
    assert.match(omitted, /Workflow <code>9007199254740995<\/code>, repository <code>9007199254740994<\/code>, source <code>fixture<\/code>/);
    assert.equal((omitted.match(/<li>/g) ?? []).length, 2);
    assertOneReportNotice(partialMarkup);
    assert.doesNotMatch(render(partial, {showHistory: true, now: Date.parse(partial.history.expires_at)}), /Retained history included for|Retained history not included for:/);
    const {history, ...timingOnly} = snapshot;
    const absent = render(timingOnly, {showHistory: true});
    assert.match(absent, /No retained history was included in this snapshot/);
    assertOneReportNotice(absent);
    assert.doesNotMatch(absent, /Most recent 7 days|Retained history has expired|Retained history included for|Retained history not included for:/);
    assertOneReportNotice(render({...timingOnly, timing: {attempts: []}}, {showHistory: true}));
    const empty = fixture();
    empty.history.workflows[0].current.initial = zeroBucket();
    const emptyMarkup = render(empty, {showHistory: true});
    assert.match(emptyMarkup, /Most recent 7 days/);
    assertOneReportNotice(emptyMarkup);
    assert.doesNotMatch(emptyMarkup, /No retained history was included/);
    const afterHistory = render(snapshot, {showHistory: true, now: Date.parse(history.expires_at)});
    assert.match(afterHistory, /Retained history has expired for this snapshot/);
    assert.match(afterHistory, /Active attempts at observation/);
    assertOneReportNotice(afterHistory);
    assert.doesNotMatch(afterHistory, /No retained history was included/);
    assert.doesNotMatch(afterHistory, /Retained workflow timing and results|Most recent 7 days/);
    const closedLease = render(snapshot);
    assert.match(closedLease, /Retained history has expired for this snapshot/);
    assertOneReportNotice(closedLease);
    assert.doesNotMatch(closedLease, /Most recent 7 days/);
    assert.equal(render(snapshot, {showHistory: true, now: Date.parse(at) + 120_000}), "");
    assert.match(markup, /not complete workflow history/);
    assert.doesNotMatch(markup, /Limited selection|Additional older retained attempts/);
    for (const [selection, label] of [["attempts", "64-attempt"], ["records", "12,000-record"]]) {
      const limited = fixture();
      limited.history.workflows[0].selection_limited = selection;
      assert.equal(validateCIHealthSnapshot(limited), limited);
      const html = render(limited, {showHistory: true});
      assert.match(html, /Limited selection/);
      assert.ok(html.includes(label + " limit"));
      assert.match(html, /No attempt was split/);
    }
    assert.match(markup, /Verified test reports are unavailable/);
    assert.doesNotMatch(markup, /Successful retry observed|No initial failures observed/);
    const recoveries = retryRecoveryFixture();
    const retryMarkup = render(recoveries, {showHistory: true});
    assert.match(retryMarkup, /Most recent 7 days: Successful retry observed for 1 of 1 observed initial failures/);
    assert.match(retryMarkup, /Previous 7 days: No initial failures observed/);
    assert.match(retryMarkup, /An unobserved retry does not establish the run’s eventual outcome/);
    assert.doesNotMatch(render(recoveries), /Successful retry observed/);
    assert.equal(render(recoveries, {showHistory: true, now: Date.parse(at) + 120_000}), "");
    assert.match(markup, /1\/2 \(50\.0%\)/);
    assert.match(markup, /Jobs \(pooled\).*<td>2 of 3<\/td><td>1 s<\/td><td>2 s<\/td>/);
    assert.match(markup, /Steps \(pooled\).*<td>1 of 1<\/td><td>0 s<\/td><td>0 s<\/td>/);
    assert.match(markup, /<td>0 of 2<\/td><td><span class="muted">Unavailable<\/span><\/td>/);
    assert.match(markup, /<td>0 of 0<\/td><td><span class="muted">Unavailable<\/span><\/td>/);
    assert.match(markup, /A completed row whose latest observation omitted its endpoints stays counted but untimed/);
    assert.match(markup, /attributed to that observation rather than refreshed as new/);
    const old = fixture();
    delete old.history.workflows[0].current.initial.jobs.completed;
    assert.equal(validateCIHealthSnapshot(old), old);
    assert.match(render(old, {showHistory: true}), /2 timed · <span class="muted">Completed count unknown<\/span>/);
    const bad = fixture();
    bad.history.workflows[0].current.initial.jobs.completed = 1;
    assert.equal(render(bad, {showHistory: true}), "");
    assert.doesNotMatch(render(snapshot), /Retained workflow timing and results/);
    assert.equal(render(snapshot, {showHistory: true, now: Date.parse(at) + 120_000}), "");
  } finally {
    try { await server?.close(); } finally { await rm(cacheDir, {recursive: true, force: true}); }
  }
});
