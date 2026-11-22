import test from "node:test";
import assert from "node:assert/strict";
import {ciHealthEndpoint, ciJobHandleHeader, ciHealthFresh, ciHealthLease, ciHealthLeaseRemaining, validateCIHealthSnapshot} from "./ci-health-client.js";
import {endpoint, ReadSession, SessionEnded, validateOverview} from "./client.js";

const token = "a".repeat(64);
const resources = {cpu_millis: 0, ram_mib: 0, runners: 0};
const overview = {
  mode: "experimental-ledger", management_enabled: false, policy_revision: "a".repeat(64),
  experimental_provisioning_enabled: false, admission_ready: false, epoch: 0,
  immutable_execution_plans: 0, legacy_unplanned_reservations: 0, legacy_source_preparations: 0, observed_at: "2026-01-01T00:00:00Z",
  global: {budget: resources, allocated: resources}, hosts: [], pools: [], states: {}, events: [],
};
const lower = {basis: "observed_lower_bound", nanoseconds: "7000000001"};
const noData = {state: "unavailable", timing: {attempts: []}};
function snapshot(at = new Date().toISOString()) {
  return {state: "observed", observed_at: at, timing: {attempts: [{
    source: "registry_1", repository_id: "9007199254740993", workflow_id: "9007199254740997",
    run_id: "9223372036854775807", attempt: "2", observed_at: at, status: "running", jobs: [{
      id: "9007199254740995", status: "queued", waiting: lower,
      steps: [{number: "3", status: "skipped"}],
    }],
  }]}};
}
const wire = (obj) => Response.json(obj);
const sessionWith = (capture) => new ReadSession((url, options) => url === endpoint
  ? Promise.resolve({ok: true, json: async () => overview})
  : capture(url, options));

test("only an explicit boolean overview marker can enable CI Health", () => {
  assert.equal(validateOverview(overview).ci_health_available, undefined);
  for (const valid of [true, false]) assert.equal(validateOverview({...overview, ci_health_available: valid}).ci_health_available, valid);
  for (const invalid of ["true", 1, null, [], {}, undefined]) {
    assert.throws(() => validateOverview({...overview, ci_health_available: invalid}), /unsupported overview/);
  }
});

test("exact wire IDs/times and feature status are kept distinct", () => {
  const at = "2026-01-01T00:00:00.123456789Z";
  const view = snapshot(at);
  assert.equal(validateCIHealthSnapshot(view).timing.attempts[0].run_id, "9223372036854775807");
  assert.equal(validateCIHealthSnapshot(noData).state, "unavailable");
  for (const coverage of ["observed_subset", "bounded_partial"]) {
    assert.equal(validateCIHealthSnapshot({...view, coverage}).coverage, coverage);
    assert.throws(() => validateCIHealthSnapshot({...noData, coverage}), /CI Health response unavailable/);
  }
  assert.equal(ciHealthFresh(view, Date.parse(at) - 1), false);
  assert.equal(ciHealthFresh(view, Date.parse(at)), true);
  assert.equal(ciHealthFresh(view, Date.parse(at) + 119_999), true);
  assert.equal(ciHealthFresh(view, Date.parse(at) + 120_000), false);
  assert.equal(ciHealthFresh(noData, Date.parse(at)), false);
});

test("accepted lease expires on the first wall or monotonic deadline, notwithstanding rollback", () => {
  const at = "2026-01-01T00:00:00Z";
  const observed = Date.parse(at);
  const view = snapshot(at);
  const lease = ciHealthLease(view, observed + 20_000, 300);
  assert.equal(ciHealthLeaseRemaining(view, lease, observed + 20_000, 300), 100_000);
  assert.equal(ciHealthLeaseRemaining(view, lease, observed + 1_000, 100_299), 1);
  assert.equal(ciHealthLeaseRemaining(view, lease, observed + 1_000, 100_300), 0);
  assert.equal(ciHealthLeaseRemaining(view, lease, observed + 120_000, 301), 0);
  assert.equal(ciHealthLeaseRemaining(view, lease, observed - 1, 301), 0);
  assert.equal(ciHealthLeaseRemaining(view, lease, observed + 20_000, 299), 0);
  assert.equal(ciHealthLease(view, observed - 1, 300), null);
  assert.equal(ciHealthLease(view, observed + 120_000, 300), null);
});

test("malformed, unsafe, unqualified or oversized timing is rejected", () => {
  const bad = [
    (x) => { x.timing.logs = []; },
    (x) => { x.coverage = "all"; },
    (x, a) => { a.log_url = "https://example.test/"; },
    (x, a) => { a.source = "https://example.test/"; },
    (x, a) => { a.run_id = Number(a.run_id); },
    (x, a) => { a.run_id = "9223372036854775808"; },
    (x, a) => { a.run_id = "01"; },
    (x, a) => { a.run_id = "1\n"; },
    (x, a, j) => { j.waiting = {...lower, nanoseconds: "-1"}; },
    (x, a, j) => { j.waiting = {...lower, basis: "estimated"}; },
    (x, a, j) => { j.execution = {basis: "observed_endpoints", nanoseconds: "0"}; },
    (x, a, j) => { j.resources = {cpu_nanoseconds: "1000", memory_bytes: "2000", disk_bytes: "3000"}; },
    (x, a, j) => { j.steps = [j.steps[0], j.steps[0]]; },
    (x, a, j) => { a.jobs = [j, j]; },
    (x, a) => { x.timing.attempts = [a, a]; },
    (x, a) => { x.timing.attempts = Array(26).fill(a); },
    (x) => { x.observed_at = "2026-02-31T00:00:00Z"; },
    (x, a) => { a.observed_at = "2026-01-01T00:00:00.123456790Z"; },
    (x) => { x.state = "unavailable"; },
  ];
  for (const mutate of bad) {
    const view = snapshot("2026-01-01T00:00:00.123456789Z");
    mutate(view, view.timing.attempts[0], view.timing.attempts[0].jobs[0]);
    assert.throws(() => validateCIHealthSnapshot(view), /CI Health response unavailable/);
  }
});

test("terminal results are optional, exact and consistent with each execution phase", () => {
  const at = "2026-01-01T00:00:00.123456789Z";
  const fixture = () => {
    const view = snapshot(at);
    const a = view.timing.attempts[0], j = a.jobs[0], s = j.steps[0];
    a.status = j.status = s.status = "completed";
    delete j.waiting;
    return {view, rows: [a, j, s]};
  };
  for (let i = 0; i < 3; i++) {
    const {view, rows} = fixture();
    assert.doesNotThrow(() => validateCIHealthSnapshot(view)); // old wire
    for (const exact of ["success", "failure", "cancelled", "timed_out", "other_terminal"]) {
      rows[i].conclusion = exact;
      assert.doesNotThrow(() => validateCIHealthSnapshot(view));
    }
    for (const other of ["", null, undefined, "Success", "skipped", "<img>", "success\n", 0]) {
      rows[i].conclusion = other;
      assert.throws(() => validateCIHealthSnapshot(view), /CI Health response unavailable/);
    }
    for (const phase of ["queued", "running", "unknown"]) {
      rows[i].status = phase;
      rows[i].conclusion = "other_terminal";
      assert.throws(() => validateCIHealthSnapshot(view), /CI Health response unavailable/);
    }
    rows[i].status = "skipped";
    rows[i].conclusion = "success";
    assert.throws(() => validateCIHealthSnapshot(view), /CI Health response unavailable/);
    rows[i].conclusion = "other_terminal";
    assert.doesNotThrow(() => validateCIHealthSnapshot(view));
  }
});

test("CI Health uses a fixed authenticated GET only after an explicit caller", async () => {
  let calls = 0;
  const session = sessionWith(async (url, options) => {
    calls++;
    assert.equal(url, ciHealthEndpoint);
    assert.equal(options.method, "GET");
    assert.equal(options.credentials, "omit");
    assert.equal(options.cache, "no-store");
    assert.equal(options.redirect, "error");
    assert.deepEqual(options.headers, {Authorization: "Bearer " + token, Accept: "application/json"});
    return wire(snapshot());
  });
  await assert.rejects(session.ciHealthSnapshot(), SessionEnded);
  await session.connect(token);
  assert.equal(calls, 0);
  const result = await session.ciHealthSnapshot();
  assert.equal(result.timing.attempts[0].repository_id, "9007199254740993");
  assert.equal(calls, 1);
});

test("absent route, withdrawn snapshot, bad payload, and read authorization loss", async () => {
  const responses = [
    new Response("", {status: 404}),
    wire(noData),
    wire(snapshot(new Date(Date.now() - 180_000).toISOString())),
    new Response(JSON.stringify(snapshot())),
    new Response(new Uint8Array([0xff]), {headers: {"content-type": "application/json"}}),
    new Response(" ".repeat((1 << 20) + 1), {headers: {"content-type": "application/json"}}),
    new Response("", {status: 401}),
  ];
  const session = sessionWith(() => responses.shift());
  await session.connect(token);
  assert.equal(await session.ciHealthSnapshot(), null);
  assert.deepEqual(await session.ciHealthSnapshot(), noData);
  assert.equal(await session.ciHealthSnapshot(), null);
  for (let i = 0; i < 3; i++) await assert.rejects(session.ciHealthSnapshot(), /CI Health response unavailable/);
  await assert.rejects(session.ciHealthSnapshot(), /Read authorization ended/);
  await assert.rejects(session.ciHealthSnapshot(), SessionEnded);
  assert.equal(responses.length, 0);
});

test("disconnect and superseding reads fence late CI results", async () => {
  const pending = [];
  const session = sessionWith((url, options) => new Promise((resolve) => pending.push({resolve, signal: options.signal})));
  await session.connect(token);
  const old = session.ciHealthSnapshot();
  const live = session.ciHealthSnapshot();
  assert.equal(pending[0].signal.aborted, true);
  pending[0].resolve(wire(snapshot()));
  await assert.rejects(old, SessionEnded);
  pending[1].resolve(wire(snapshot()));
  assert.equal((await live).state, "observed");
  const revoked = session.ciHealthSnapshot();
  session.disconnect();
  assert.equal(pending[2].signal.aborted, true);
  pending[2].resolve(wire(snapshot()));
  await assert.rejects(revoked, SessionEnded);
});

test("caller cancellation aborts a pending body without waiting for its producer", async () => {
  const stream = new ReadableStream({start() {}});
  const session = sessionWith(() => new Response(stream, {headers: {"content-type": "application/json"}}));
  await session.connect(token);
  const controller = new AbortController();
  const pending = session.ciHealthSnapshot({signal: controller.signal});
  await Promise.resolve();
  controller.abort();
  await assert.rejects(pending, SessionEnded);
});

test("optional creation-to-start is an independent exact duration for started jobs", () => {
  const exact = {basis: "observed_endpoints", nanoseconds: "0"};
  for (const status of ["running", "completed", "queued", "skipped", "unknown"]) {
    const view = snapshot();
    const [a] = view.timing.attempts;
    a.jobs = [{id: "9007199254740995", status, steps: []}];
    assert.doesNotThrow(() => validateCIHealthSnapshot(view));
    a.jobs[0].creation_to_start = exact;
    if (status === "running" || status === "completed") {
      assert.equal(validateCIHealthSnapshot(view).timing.attempts[0].jobs[0].creation_to_start.nanoseconds, "0");
      assert.equal(a.jobs[0].queue, undefined);
      for (const invalid of [null, lower, {...exact, nanoseconds: "9223372036854775808"}]) {
        a.jobs[0].creation_to_start = invalid;
        assert.throws(() => validateCIHealthSnapshot(view), /CI Health response unavailable/);
      }
    } else {
      assert.throws(() => validateCIHealthSnapshot(view), /CI Health response unavailable/);
    }
  }
});

const exactJobSelection = {
  handle: "A".repeat(43), source: "registry_1", repository_id: "9007199254740993", workflow_id: "9007199254740997",
  run_id: "9223372036854775807", attempt: "2", job_id: "9007199254740995",
};
const {handle: ignoredFixtureHandle, ...exactJobResponseIDs} = exactJobSelection;
const exactJobHref = "https://github.com/synthetic/fixture/actions/runs/9223372036854775807/job/9007199254740995";

test("CI job owner lookup validates origin, exact identity, and response bounds", async () => {
  const responses = [
    wire({...exactJobResponseIDs, url: exactJobHref}),
    wire({...exactJobResponseIDs, attempt: "1", url: exactJobHref}),
    wire({...exactJobResponseIDs, url: exactJobHref.replace("github.com", "github.com@other.invalid")}),
    wire({...exactJobResponseIDs, url: exactJobHref + "?redirect=1"}),
    wire({...exactJobResponseIDs, url: exactJobHref.replace("/job/", "/runs/")}),
    wire({...exactJobResponseIDs, url: exactJobHref.replace(/5$/, "6")}),
    new Response(" ".repeat(1025), {headers: {"content-type": "application/json"}}),
    new Response("", {status: 404}),
    new Response("", {status: 401}),
  ];
  const session = sessionWith(async (url, options) => {
    assert.equal(url, ciHealthEndpoint);
    assert.equal(options.method, "GET");
    assert.equal(options.credentials, "omit");
    assert.equal(options.cache, "no-store");
    assert.equal(options.redirect, "error");
    assert.deepEqual(options.headers, {Authorization: "Bearer " + token, Accept: "application/json", [ciJobHandleHeader]: exactJobSelection.handle});
    return responses.shift();
  });
  await assert.rejects(session.ciHealthJobDestination(exactJobSelection), SessionEnded);
  await session.connect(token);
  assert.equal(await session.ciHealthJobDestination({...exactJobSelection, handle: "bad"}), null);
  assert.equal(await session.ciHealthJobDestination(exactJobSelection), exactJobHref);
  for (let i = 0; i < 7; i++) assert.equal(await session.ciHealthJobDestination(exactJobSelection), null);
  await assert.rejects(session.ciHealthJobDestination(exactJobSelection), /Read authorization ended/);
  await assert.rejects(session.ciHealthJobDestination(exactJobSelection), SessionEnded);
  assert.equal(responses.length, 0);
});

test("timing refresh and disconnect cancel late job resolutions", async () => {
  const pending = [];
  const session = sessionWith((url, options) => new Promise((resolve) => pending.push({resolve, signal: options.signal})));
  await session.connect(token);
  const click = session.ciHealthJobDestination(exactJobSelection);
  const refresh = session.ciHealthSnapshot();
  assert.equal(pending[0].signal.aborted, true);
  pending[0].resolve(wire({...exactJobResponseIDs, url: exactJobHref}));
  await assert.rejects(click, SessionEnded);
  pending[1].resolve(wire(snapshot()));
  assert.equal((await refresh).state, "observed");
  const revoked = session.ciHealthJobDestination(exactJobSelection);
  session.disconnect();
  assert.equal(pending[2].signal.aborted, true);
  pending[2].resolve(wire({...exactJobResponseIDs, url: exactJobHref}));
  await assert.rejects(revoked, SessionEnded);
});

test("current timing can carry a unique handle but never a URL", () => {
  const view = snapshot();
  const jobs = view.timing.attempts[0].jobs;
  jobs[0].job_link_handle = exactJobSelection.handle;
  assert.equal(validateCIHealthSnapshot(view), view);
  jobs[0].url = exactJobHref;
  assert.throws(() => validateCIHealthSnapshot(view), /unavailable/);
  delete jobs[0].url;
  jobs.push({...jobs[0], id: "9007199254740996"});
  assert.throws(() => validateCIHealthSnapshot(view), /unavailable/);
  jobs[1].job_link_handle = "A".repeat(42) + "B";
  assert.throws(() => validateCIHealthSnapshot(view), /unavailable/);
});
