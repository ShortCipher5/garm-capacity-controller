import test from "node:test";
import assert from "node:assert/strict";
import {mkdtemp, rm} from "node:fs/promises";
import {tmpdir} from "node:os";
import {join} from "node:path";
import React from "react";
import {renderToStaticMarkup} from "react-dom/server";
import {createServer} from "vite";
import {ciByteText, ciUsageCoverage, ciUsageReasonText, ciUsageStateLabel, validateCIJobResourceUsage} from "./ci-health-usage.js";
import {validateCIHealthSnapshot} from "./ci-health-client.js";

// These fixtures mirror the JSON projection asserted in
// internal/cihealth/verified_usage_test.go: the web contract and the Go
// contract are the same wire shape.
const measured = {
  samples: 3,
  window_ns: "6000000000",
  cpu: {covered_ns: "6000000000", complete: true, average_millicpu: "1000", peak_interval_millicpu: "1250"},
  ram: {covered_ns: "6000000000", complete: true, mean_bytes: "33", sample_peak_bytes: "50", gauge_samples: 3},
  disk: {state: "unavailable", reason: "disk_not_independently_measured"},
};

test("resource usage wire accepts every documented state", () => {
  assert.ok(validateCIJobResourceUsage({state: "verified", as_of: "2026-01-02T03:04:35Z", measured}));
  assert.ok(validateCIJobResourceUsage({state: "partial", as_of: "2026-01-02T03:04:35Z", measured: {...measured, cpu: {covered_ns: "2000000000", complete: false, average_millicpu: "500", peak_interval_millicpu: "500"}}}));
  assert.ok(validateCIJobResourceUsage({state: "unavailable", as_of: "2026-01-02T03:04:35Z", reason: "provider_origin_join_unsupported"}));
  assert.ok(validateCIJobResourceUsage({state: "unavailable", as_of: "2026-01-02T03:04:35Z", reason: "collection_unavailable"}));
  assert.ok(validateCIJobResourceUsage({state: "stale", as_of: "2026-01-02T03:04:35Z", reason: "stale_evidence"}));
  assert.ok(validateCIJobResourceUsage({state: "partial", as_of: "2026-01-02T03:04:35Z", measured: {...measured, disk: {state: "verified", read_bytes: "50", covered_ns: "6000000000", complete: true}}}));
  assert.ok(validateCIJobResourceUsage({state: "partial", as_of: "2026-01-02T03:04:35Z", measured: {...measured, disk: {state: "unavailable", reason: "disk_unsupported_by_sources"}}}));
  assert.ok(validateCIJobResourceUsage({state: "unavailable", as_of: null, reason: "provider_origin_join_unsupported"}));
  assert.equal(validateCIJobResourceUsage({state: "verified", as_of: null, measured}), null);
  assert.equal(validateCIJobResourceUsage({state: "partial", as_of: null, measured}), null);
  // Single-gauge RAM without a time-weighted mean stays valid.
  assert.ok(validateCIJobResourceUsage({state: "verified", as_of: "2026-01-02T03:04:35Z", measured: {...measured, ram: {sample_peak_bytes: "50", gauge_samples: 3}}}));
});

test("resource usage wire rejects malformed metric values at the boundary", () => {
  const reject = (value) => assert.equal(validateCIJobResourceUsage(value), null);
  const at = "2026-01-02T03:04:35Z";
  const cpu = (over = {}) => ({covered_ns: "6000000000", complete: true, average_millicpu: "1000", peak_interval_millicpu: "1250", ...over});
  const ram = (over = {}) => ({covered_ns: "6000000000", complete: true, mean_bytes: "33", sample_peak_bytes: "50", gauge_samples: 3, ...over});
  const disk = (over = {}) => ({state: "unavailable", reason: "disk_not_independently_measured", ...over});
  const usage = (measuredOver = {}, cpuOver, ramOver, diskOver) => validateCIJobResourceUsage({
    state: "verified", as_of: at,
    measured: {samples: 3, window_ns: "6000000000", cpu: cpu(cpuOver), ram: ram(ramOver), disk: disk(diskOver), ...measuredOver},
  });
  // Sample counts: measured usage with zero samples invents a series.
  assert.equal(usage({samples: 0}), null);
  assert.equal(usage({samples: 4097}), null);
  // Coverage can never exceed the window, and complete must mean exactly full.
  assert.ok(usage());
  assert.equal(usage({}, {covered_ns: "6000000001"}), null);
  assert.equal(usage({}, {covered_ns: "2000000000"}), null); // complete but not full
  assert.equal(usage({}, {covered_ns: "6000000000", complete: false}), null); // full but not complete
  // CPU average cannot exceed the interval peak.
  assert.equal(usage({}, {average_millicpu: "1251"}), null);
  assert.equal(usage({}, {average_millicpu: "2000", peak_interval_millicpu: "1000"}), null);
  // RAM coverage consistency and gauge count bounded by the sample series.
  assert.equal(usage({}, undefined, {covered_ns: "7000000000"}), null);
  assert.equal(usage({}, undefined, {covered_ns: "6000000000", complete: false}), null);
  assert.equal(usage({}, undefined, {gauge_samples: 4}), null);
  assert.equal(usage({}, undefined, {gauge_samples: 0}), null);
  // Disk coverage and completeness must agree with the window too.
  assert.equal(usage({}, undefined, undefined, {state: "verified", read_bytes: "50", covered_ns: "6000000001"}), null);
  assert.equal(usage({}, undefined, undefined, {state: "verified", read_bytes: "50", covered_ns: "2000000000", complete: true}), null);
  assert.equal(usage({}, undefined, undefined, {state: "verified", read_bytes: "50", covered_ns: "6000000000", complete: false}), null);
});

test("resource usage wire rejects hidden or invented numbers", () => {
  const reject = (value) => assert.equal(validateCIJobResourceUsage(value), null);
  reject(null);
  reject({state: "unknown", as_of: "2026-01-02T03:04:35Z"});
  reject({state: "verified", as_of: "2026-01-02T03:04:35Z"}); // measured required
  reject({state: "verified", as_of: "2026-01-02T03:04:35Z", reason: "counter_discontinuity", measured}); // reason forbidden
  reject({state: "partial", as_of: "not-a-time", measured});
  reject({state: "unavailable", as_of: "2026-01-02T03:04:35Z"}); // reason required
  reject({state: "stale", as_of: "2026-01-02T03:04:35Z", measured, reason: "stale_evidence"}); // no numbers when stale
  reject({state: "unavailable", as_of: "2026-01-02T03:04:35Z", reason: "provider_origin_join_unsupported", measured});
  reject({state: "verified", as_of: "2026-01-02T03:04:35Z", measured: {...measured, window_ns: 6000000000}}); // narrowed number
  reject({state: "verified", as_of: "2026-01-02T03:04:35Z", measured: {...measured, samples: "3"}});
  reject({state: "verified", as_of: "2026-01-02T03:04:35Z", measured: {...measured, cpu: {covered_ns: "6000000000", complete: true, average_millicpu: "1000"}}}); // missing peak
  reject({state: "verified", as_of: "2026-01-02T03:04:35Z", measured: {...measured, ram: {covered_ns: "6000000000", complete: true}}}); // mean missing
  reject({state: "verified", as_of: "2026-01-02T03:04:35Z", measured: {...measured, ram: {sample_peak_bytes: "50"}}}); // gauge count missing
  reject({state: "verified", as_of: "2026-01-02T03:04:35Z", measured: {...measured, disk: {state: "verified"}}}); // disk without quantities
  reject({state: "verified", as_of: "2026-01-02T03:04:35Z", measured: {...measured, disk: {state: "unavailable", reason: "disk_not_independently_measured", read_bytes: "50"}}});
  reject({state: "unavailable", as_of: "2026-01-02T03:04:35Z", reason: "not_a_reason"});
});

test("usage display helpers format coverage without narrowing", () => {
  assert.equal(ciUsageCoverage("6000000000", "6000000000"), "100%");
  assert.equal(ciUsageCoverage("2000000000", "6000000000"), "33%");
  assert.equal(ciUsageCoverage("1", "6000000000"), "0%");
  assert.equal(ciUsageCoverage("1", "0"), null);
  assert.equal(ciByteText("0"), "0 B");
  assert.equal(ciByteText("2048"), "2 KiB");
  assert.equal(ciByteText("5368709120"), "5 GiB");
  assert.equal(ciUsageStateLabel("verified"), "Verified");
  assert.equal(ciUsageStateLabel("partial"), "Partial");
  assert.equal(ciUsageStateLabel("stale"), "Stale");
  assert.equal(ciUsageStateLabel("unavailable"), "Unavailable");
  assert.match(ciUsageReasonText("provider_origin_join_unsupported"), /original provider allocation/);
  assert.match(ciUsageReasonText("job_too_short_uncaptured"), /not zero/);
  assert.match(ciUsageReasonText("disk_not_independently_measured"), /host free disk/);
});

function usageSnapshot(jobUsage) {
  return {
    state: "observed", observed_at: "2026-01-02T03:04:35Z",
    timing: {attempts: [{source: "fixture_source", repository_id: "9007199254740993", workflow_id: "17", run_id: "9223372036854775807", attempt: "2", observed_at: "2026-01-02T03:04:35Z", status: "completed", jobs: [{id: "9007199254741001", status: "completed", steps: [], ...(jobUsage ? {resource_usage: jobUsage} : {})}]}]},
  };
}

test("ci-health snapshot admits exact per-job usage states and rejects corrupt ones", () => {
  assert.doesNotThrow(() => validateCIHealthSnapshot(usageSnapshot(null)));
  assert.doesNotThrow(() => validateCIHealthSnapshot(usageSnapshot({state: "unavailable", as_of: "2026-01-02T03:04:35Z", reason: "provider_origin_join_unsupported"})));
  assert.doesNotThrow(() => validateCIHealthSnapshot(usageSnapshot({state: "verified", as_of: "2026-01-02T03:04:35Z", measured})));
  assert.throws(() => validateCIHealthSnapshot(usageSnapshot({state: "verified", as_of: "2026-01-02T03:04:35Z"})), /unavailable/);
  assert.throws(() => validateCIHealthSnapshot(usageSnapshot({state: "partial", as_of: "2026-01-02T03:04:35Z", measured: {...measured, samples: 5000}})), /unavailable/);
});

test("ci-health timing renders per-job usage states without inventing zeros", async () => {
  const cacheDir = await mkdtemp(join(tmpdir(), "garm-ci-health-usage-render-"));
  let server;
  try {
    server = await createServer({cacheDir, server: {middlewareMode: true, hmr: false}, appType: "custom"});
    const {CIHealthTiming} = await server.ssrLoadModule("/src/ci-health.jsx");
    const render = (snapshot) => renderToStaticMarkup(React.createElement(CIHealthTiming, {snapshot, now: Date.parse("2026-01-02T03:04:36Z")}));

    const none = render(usageSnapshot(null));
    assert.match(none, /Per-job CPU, memory, and disk usage are unavailable: no documented source proves the runner.{0,8}s original provider allocation/);
    assert.doesNotMatch(none, /<dt>Resource usage<\/dt>/);

    const verified = render(usageSnapshot({state: "verified", as_of: "2026-01-02T03:04:35Z", measured}));
    assert.match(verified, /<dt>Resource usage<\/dt><dd><span class="pill">Verified<\/span>/);
    assert.match(verified, /average 1000 mCPU, interval peak 1250 mCPU/);
    assert.match(verified, /time-weighted mean 33 B, sampled peak 50 B/);
    assert.match(verified, /host free disk is not job usage/);
    assert.match(verified, /3 over 100% of the job window/);
    assert.doesNotMatch(verified, /no documented source proves the runner.{0,8}s original provider allocation/);

    const partial = render(usageSnapshot({state: "partial", as_of: "2026-01-02T03:04:35Z", measured: {...measured, cpu: {covered_ns: "2000000000", complete: false, average_millicpu: "500", peak_interval_millicpu: "500"}}}));
    assert.match(partial, /<span class="pill">Partial<\/span>/);
    assert.match(partial, /average 500 mCPU, interval peak 500 mCPU \(partial coverage\)/);
    assert.match(partial, /3 over 33% of the job window/);

    const stale = render(usageSnapshot({state: "stale", as_of: "2026-01-02T03:04:35Z", reason: "stale_evidence"}));
    assert.match(stale, /<span class="pill">Stale<\/span>/);
    assert.match(stale, /observation expired/);
    assert.doesNotMatch(stale, /average \d+ mCPU/);

    const unavailable = render(usageSnapshot({state: "unavailable", as_of: "2026-01-02T03:04:35Z", reason: "provider_origin_join_unsupported"}));
    assert.match(unavailable, /<span class="pill">Unavailable<\/span>/);
    assert.match(unavailable, /No documented source proves the runner.{0,8}s original provider allocation/);
    assert.doesNotMatch(unavailable, /mCPU/);
  } finally {
    if (server) await server.close();
    await rm(cacheDir, {recursive: true, force: true});
  }
});
