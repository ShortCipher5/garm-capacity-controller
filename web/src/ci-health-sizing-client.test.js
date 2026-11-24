import test from "node:test";
import assert from "node:assert/strict";
import {ciSizingEndpoint, ciSizingFresh, ciSizingLease, ciSizingLeaseRemaining, validateCISizingSnapshot} from "./ci-health-sizing-client.js";
import {endpoint, ReadSession, SessionEnded, validateOverview} from "./client.js";

const token = "a".repeat(64);
const resources = {cpu_millis: 0, ram_mib: 0, runners: 0};
const overview = {
  mode: "experimental-ledger", management_enabled: false, policy_revision: "a".repeat(64),
  experimental_provisioning_enabled: false, admission_ready: false, epoch: 0,
  immutable_execution_plans: 0, legacy_unplanned_reservations: 0, legacy_source_preparations: 0, observed_at: "2026-01-01T00:00:00Z",
  global: {budget: resources, allocated: resources}, hosts: [], pools: [], states: {}, events: [],
};
const noData = {state: "unavailable", sizing: null};
const at = "2026-01-01T00:00:00Z";

function snapshot(overrides = {}) {
  const sizing = {
    policy_version: "job-sizing-cohort-policy/v1", advisory_version: "job-sizing-advisory/v1",
    outcome: "increase_advised",
    scope: {source: "registry_1", repository_id: "9007199254740993", workflow_id: "17"},
    identity: {matrix: "", profile: "small", revision: "rev-1"},
    window: {from: "2025-12-25T00:00:00Z", as_of: at, latest_completion_max_age_ns: "3600000000000"},
    freshness: {latest_primary_completion: "2025-12-31T23:00:00Z", age_ns: "3600000000000", fresh: true},
    coverage: "complete",
    samples: {eligible: 20, primary: 20, retried: 0, successful: 20, at_risk: 0, unknown: 0, unsuccessful: 0, throttled: 0, oom: 0, disk_full: 0, external_builder: 0, qualifying_cpu: 20, qualifying_ram: 20},
    observed: {cpu_peak_milli: "900", ram_peak_bytes: "96", cpu_saturated: 3, ram_saturated: 0},
    installed: {cpu_milli: "1000", ram_bytes: "100"},
    cpu: {state: "increase_advised", suggested: "1125", reason: ""},
    ram: {state: "unchanged", suggested: null, reason: ""},
    uncertainty: "sampled_peak_bound",
    host: {known: true, host: "host-a", cpu_budget_milli: "64000", cpu_allocated_milli: "4000", cpu_headroom_milli: "60000", ram_budget_bytes: "68719476736", ram_allocated_bytes: "4294967296", ram_headroom_bytes: "64424509440", fit: "fits"},
    warnings: ["sustained_cpu_pressure"],
    disk: {state: "unavailable", peak_bytes: null, samples: 0, qualified: 20},
    rejected: [{source: "registry_1", repository_id: "9007199254740993", workflow_id: "17", run_id: "7", attempt: "1", job_id: "100", reason: "stale"}],
    incomparable: 2,
    ...overrides,
  };
  return {state: "observed", observed_at: at, sizing};
}

test("only an explicit boolean overview marker can enable advisory sizing", () => {
  assert.equal(validateOverview(overview).ci_health_sizing_available, undefined);
  for (const valid of [true, false]) assert.equal(validateOverview({...overview, ci_health_sizing_available: valid}).ci_health_sizing_available, valid);
  for (const invalid of ["true", 1, null, [], {}, undefined]) {
    assert.throws(() => validateOverview({...overview, ci_health_sizing_available: invalid}), /unsupported overview/);
  }
});

test("exact advisory wire is validated and unavailable stays featureless", () => {
  assert.equal(validateCISizingSnapshot(noData).state, "unavailable");
  assert.throws(() => validateCISizingSnapshot({...noData, observed_at: at}), /CI sizing response unavailable/);
  assert.throws(() => validateCISizingSnapshot({...noData, sizing: {}}), /CI sizing response unavailable/);
  assert.equal(validateCISizingSnapshot(snapshot()).sizing.cpu.suggested, "1125");
  assert.equal(ciSizingFresh(snapshot(), Date.parse(at)), true);
  assert.equal(ciSizingFresh(snapshot(), Date.parse(at) + 120_000), false);
  assert.equal(ciSizingFresh(noData, Date.parse(at)), false);
});

test("suggestions appear only with an advised increase and reasons only with a blocker", () => {
  const bad = (sizing) => assert.throws(() => validateCISizingSnapshot(snapshot(sizing)), /CI sizing response unavailable/);
  bad({cpu: {state: "unchanged", suggested: "1125", reason: ""}});
  bad({cpu: {state: "increase_advised", suggested: null, reason: ""}});
  bad({cpu: {state: "increase_advised", suggested: "1125", reason: "not_reviewed"}});
  bad({cpu: {state: "stale_history", suggested: null, reason: ""}});
  bad({cpu: {state: "coverage_unavailable", suggested: null, reason: "bogus_reason"}});
  assert.equal(validateCISizingSnapshot(snapshot({cpu: {state: "stale_history", suggested: null, reason: "no_fresh_primary_completion"}})).sizing.cpu.state, "stale_history");
});

test("missing facts are null, unknown states and relaxed counts reject", () => {
  const bad = (sizing) => assert.throws(() => validateCISizingSnapshot(snapshot(sizing)), /CI sizing response unavailable/);
  bad({outcome: "downsize_advised"});
  bad({coverage: "total"});
  bad({uncertainty: "exact_bound"});
  bad({uncertainty: ""});
  const noPeaks = validateCISizingSnapshot(snapshot({observed: {cpu_peak_milli: null, ram_peak_bytes: null, cpu_saturated: 0, ram_saturated: 0}, uncertainty: ""}));
  assert.equal(noPeaks.sizing.observed.cpu_peak_milli, null);
  bad({freshness: {latest_primary_completion: null, age_ns: "0", fresh: true}});
  bad({freshness: {latest_primary_completion: "2026-01-02T00:00:00Z", age_ns: "1", fresh: true}});
  bad({samples: {eligible: 20, primary: 19, retried: 0, successful: 20, at_risk: 0, unknown: 0, unsuccessful: 0, throttled: 0, oom: 0, disk_full: 0, external_builder: 0, qualifying_cpu: 20, qualifying_ram: 20}});
  bad({samples: {eligible: 20, primary: 20, retried: 0, successful: 20, at_risk: 0, unknown: 0, unsuccessful: 0, throttled: 0, oom: 0, disk_full: 0, external_builder: 0, qualifying_cpu: 21, qualifying_ram: 20}});
  bad({host: {known: false, host: "", cpu_budget_milli: "1", cpu_allocated_milli: null, cpu_headroom_milli: null, ram_budget_bytes: null, ram_allocated_bytes: null, ram_headroom_bytes: null, fit: "unknown"}});
  bad({host: {known: true, host: "host-a", cpu_budget_milli: "64000", cpu_allocated_milli: "4000", cpu_headroom_milli: "60000", ram_budget_bytes: "68719476736", ram_allocated_bytes: "4294967296", ram_headroom_bytes: "64424509440", fit: "unknown"}});
  bad({warnings: ["sustained_cpu_pressure", "sustained_cpu_pressure"]});
  bad({disk: {state: "observed", peak_bytes: null, samples: 1, qualified: 20}});
  bad({disk: {state: "unavailable", peak_bytes: "10", samples: 0, qualified: 20}});
  bad({rejected: [{source: "registry_1", repository_id: "9007199254740993", workflow_id: "17", run_id: "7", attempt: "1", job_id: "100", reason: "joined_anyway"}]});
  bad({window: {from: at, as_of: at, latest_completion_max_age_ns: "3600000000000"}});
  const unknownHost = snapshot({host: {known: false, host: "", cpu_budget_milli: null, cpu_allocated_milli: null, cpu_headroom_milli: null, ram_budget_bytes: null, ram_allocated_bytes: null, ram_headroom_bytes: null, fit: "unknown"}});
  assert.equal(validateCISizingSnapshot(unknownHost).sizing.host.known, false);
});

test("sizing read is authorized, optional and fails closed", async () => {
  const calls = [];
  const session = new ReadSession(async (url, options) => {
    calls.push([url, options]);
    if (url === endpoint) return {ok: true, json: async () => overview};
    return {status: 404, ok: false};
  });
  await session.connect(token);
  assert.equal(await session.ciHealthSizingSnapshot(), null);
  assert.equal(calls.at(-1)[0], ciSizingEndpoint);
  assert.equal(calls.at(-1)[1].headers.Authorization, `Bearer ${token}`);
  const unavailable = new ReadSession(async (url) => url === endpoint
    ? {ok: true, json: async () => overview}
    : Response.json(noData));
  await unavailable.connect(token);
  assert.equal((await unavailable.ciHealthSizingSnapshot()).state, "unavailable");
  const rejected = new ReadSession(async (url) => url === endpoint
    ? {ok: true, json: async () => overview}
    : {ok: false, status: 500});
  await rejected.connect(token);
  await assert.rejects(() => rejected.ciHealthSizingSnapshot(), /CI sizing response unavailable/);
});

test("accepted lease expires on the first wall or monotonic deadline", () => {
  const observed = Date.parse(at);
  const view = snapshot();
  const lease = ciSizingLease(view, observed + 20_000, 300);
  assert.equal(ciSizingLeaseRemaining(view, lease, observed + 20_000, 300), 100_000);
  assert.equal(ciSizingLeaseRemaining(view, null, observed, 300), 0);
  assert.equal(ciSizingLease(view, observed - 1, 300), null);
});
