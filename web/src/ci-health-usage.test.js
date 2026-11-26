import test from "node:test";
import assert from "node:assert/strict";
import {mkdtemp, rm} from "node:fs/promises";
import {tmpdir} from "node:os";
import {join} from "node:path";
import React from "react";
import {renderToStaticMarkup} from "react-dom/server";
import {createServer} from "vite";
import {ciByteText, ciUsageCoverage, ciUsageDiskView, ciUsageReasonText, ciUsageStateLabel, validateCIJobResourceUsage} from "./ci-health-usage.js";
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

test("resource usage wire admits the versioned split disk shape", () => {
  const at = "2026-01-02T03:04:35Z";
  const usage = (disk) => validateCIJobResourceUsage({state: "verified", as_of: at, measured: {...measured, disk}});
  const counter = (over = {}) => ({bytes: "50", covered_ns: "6000000000", complete: true, ...over});
  // Read only, write only, both with differing windows, gauge only, and a
  // measured zero are all valid when each part is internally consistent.
  assert.ok(usage({state: "verified", version: "disk-measured/v2", read: counter()}));
  assert.ok(usage({state: "verified", version: "disk-measured/v2", write: counter({bytes: "0"})}));
  assert.ok(usage({state: "verified", version: "disk-measured/v2",
    read: counter(), write: counter({bytes: "3", covered_ns: "4000000000", complete: false})}));
  assert.ok(usage({state: "verified", version: "disk-measured/v2",
    used: {sample_peak_bytes: "100", gauge_samples: 3, peak_at: "2026-01-02T03:04:30Z", latest_sample_at: at}}));
  assert.ok(usage({state: "verified", version: "disk-measured/v2", used: {sample_peak_bytes: "0", gauge_samples: 1, peak_at: at, latest_sample_at: at}}));
  assert.ok(usage({state: "verified", version: "disk-measured/v2", write: counter({bytes: "18446744073709551615"})}));
});

test("resource usage wire rejects corrupt versioned disk shapes", () => {
  const at = "2026-01-02T03:04:35Z";
  const reject = (disk) => assert.equal(validateCIJobResourceUsage({state: "verified", as_of: at, measured: {...measured, disk}}), null);
  const counter = (over = {}) => ({bytes: "50", covered_ns: "6000000000", complete: true, ...over});
  reject({state: "verified", version: "disk-measured/v2"}); // no measured part
  reject({state: "verified", version: "disk-measured/v3", read: counter()}); // unknown version
  reject({state: "verified", version: "disk-measured/v2", read: counter(), read_bytes: "50"}); // mixed shapes
  reject({state: "verified", read_bytes: "50", read: counter()}); // unversioned mixed shape
  reject({state: "verified", version: "disk-measured/v2", read: counter({covered_ns: "0", complete: false})});
  reject({state: "verified", version: "disk-measured/v2", read: counter({bytes: "18446744073709551616"})});
  reject({state: "verified", version: "disk-measured/v2", read: counter({covered_ns: "6000000001"})}); // coverage beyond window
  reject({state: "verified", version: "disk-measured/v2", read: counter({complete: false})}); // full but not complete
  reject({state: "verified", version: "disk-measured/v2", write: counter({covered_ns: "2000000000", complete: true})}); // complete but not full
  reject({state: "verified", version: "disk-measured/v2", read: {bytes: "50", covered_ns: "6000000000"}}); // missing completeness
  const gauge = (over = {}) => ({state: "verified", version: "disk-measured/v2", used: {
    sample_peak_bytes: "100", gauge_samples: 3,
    peak_at: "2026-01-02T03:04:30.000000001Z", latest_sample_at: "2026-01-02T03:04:30.000000002Z", ...over,
  }});
  assert.ok(validateCIJobResourceUsage({state: "verified", as_of: at, measured: {...measured, disk: gauge()}}));
  reject(gauge({gauge_samples: 4}));
  reject(gauge({gauge_samples: 0}));
  reject(gauge({gauge_samples: 1}));
  reject(gauge({peak_at: "not-a-time"}));
  reject(gauge({peak_at: "2025-02-29T03:04:30Z"}));
  reject(gauge({peak_at: "0001-01-01T00:00:00Z"}));
  reject(gauge({peak_at: "2026-01-02T03:04:20Z"}));
  reject(gauge({peak_at: "2026-01-02T03:04:30.000000003Z"}));
  reject(gauge({latest_sample_at: "2026-01-02T03:04:35.000000001Z"}));
  reject(gauge({covered_ns: "6000000000"}));
  const missingTime = gauge();
  delete missingTime.used.latest_sample_at;
  reject(missingTime);
  assert.equal(validateCIJobResourceUsage({state: "partial", as_of: at, measured: {
    samples: 1, window_ns: "6000000000",
    disk: {state: "verified", version: "disk-measured/v2", read: counter()},
  }}), null);
  reject({state: "verified", version: "disk-measured/v2", read: counter(), reason: "disk_not_independently_measured"}); // measured carries no reason
});

test("resource usage wire keeps the legacy mixed disk shape readable without lending read coverage to writes", () => {
  const at = "2026-01-02T03:04:35Z";
  const usage = (disk) => validateCIJobResourceUsage({state: "partial", as_of: at, measured: {...measured, disk}});
  // Historical payloads stay decodable.
  assert.ok(usage({state: "verified", read_bytes: "50", covered_ns: "6000000000", complete: true}));
  assert.ok(usage({state: "verified", read_bytes: "50", write_bytes: "3", covered_ns: "6000000000", complete: true}));
  assert.ok(usage({state: "verified", write_bytes: "3", used_sample_peak_bytes: "100"}));
  assert.ok(usage({state: "verified", read_bytes: "18446744073709551615", covered_ns: "2000000000"}));
  assert.ok(usage({state: "verified", read_bytes: "50", covered_ns: "2000000000"}));
  assert.equal(usage({state: "verified", read_bytes: "50", covered_ns: "0"}), null);
  assert.equal(usage({state: "verified", read_bytes: "50"}), null);
  // Orphaned legacy coverage without the read counter it was measured on is
  // malformed, not a write measurement.
  assert.equal(usage({state: "verified", write_bytes: "3", covered_ns: "6000000000", complete: true}), null);
  // The normalized view assigns the legacy shared coverage to reads only.
  const legacy = ciUsageDiskView({state: "verified", read_bytes: "50", write_bytes: "3", covered_ns: "6000000000", complete: true});
  assert.deepEqual(legacy.read, {bytes: "50", covered: "6000000000", complete: true});
  assert.deepEqual(legacy.write, {bytes: "3", covered: null, complete: null});
  const versioned = ciUsageDiskView({state: "verified", version: "disk-measured/v2",
    read: {bytes: "50", covered_ns: "6000000000", complete: true},
    used: {sample_peak_bytes: "100", gauge_samples: 3, peak_at: "2026-01-02T03:04:30Z", latest_sample_at: at}});
  assert.deepEqual(versioned.read, {bytes: "50", covered: "6000000000", complete: true});
  assert.equal(versioned.write, null);
  assert.deepEqual(versioned.used, {peak: "100", samples: 3, peakAt: "2026-01-02T03:04:30Z", latestAt: at});
  assert.equal(ciUsageDiskView({state: "unavailable", reason: "disk_unsupported_by_sources"}).state, "unavailable");
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
    state: "observed", observed_at: "2026-01-02T03:04:35Z", ...(jobUsage ? {usage_expires_at: "2026-01-02T03:05:00Z"} : {}),
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
    const render = (snapshot) => renderToStaticMarkup(React.createElement(CIHealthTiming, {snapshot, showUsage: true, now: Date.parse("2026-01-02T03:04:36Z")}));

    const none = render(usageSnapshot(null));
    assert.match(none, /Per-job CPU, memory, and disk usage are unavailable: no documented source proves the runner.{0,8}s original provider allocation/);
    assert.doesNotMatch(none, /<dt>Resource usage<\/dt>/);

    const verified = render(usageSnapshot({state: "verified", as_of: "2026-01-02T03:04:35Z", measured}));
    assert.match(verified, /<dt>Resource usage<\/dt><dd><span class="pill">Verified<\/span>/);
    assert.match(verified, /average 1000 mCPU, interval peak 1250 mCPU/);
    assert.match(verified, /time-weighted mean 33 B over 100% of the job window, sampled peak 50 B/);
    assert.match(verified, /host free disk is not job usage/);
    assert.match(verified, /<dt>Samples<\/dt><dd>3<\/dd>/);
    assert.doesNotMatch(verified, /no documented source proves the runner.{0,8}s original provider allocation/);

    const partial = render(usageSnapshot({state: "partial", as_of: "2026-01-02T03:04:35Z", measured: {...measured, cpu: {covered_ns: "2000000000", complete: false, average_millicpu: "500", peak_interval_millicpu: "500"}}}));
    assert.match(partial, /<span class="pill">Partial<\/span>/);
    assert.match(partial, /average 500 mCPU, interval peak 500 mCPU over 33% of the job window \(partial coverage\)/);

    const stale = render(usageSnapshot({state: "stale", as_of: "2026-01-02T03:04:35Z", reason: "stale_evidence"}));
    assert.match(stale, /<span class="pill">Stale<\/span>/);
    assert.match(stale, /observation expired/);
    assert.doesNotMatch(stale, /average \d+ mCPU/);

    const splitDisk = render(usageSnapshot({state: "verified", as_of: "2026-01-02T03:04:35Z", measured: {...measured,
      disk: {state: "verified", version: "disk-measured/v2",
        read: {bytes: "5368709120", covered_ns: "6000000000", complete: true},
        write: {bytes: "2048", covered_ns: "4000000000", complete: false},
        used: {sample_peak_bytes: "1048576", gauge_samples: 3, peak_at: "2026-01-02T03:04:30Z", latest_sample_at: "2026-01-02T03:04:35Z"}}}}));
    assert.match(splitDisk, /<dt>Disk read<\/dt><dd>5 GiB over 100% of the job window<\/dd>/);
    assert.match(splitDisk, /<dt>Disk written<\/dt><dd>2 KiB over 66% of the job window \(partial coverage\)<\/dd>/);
    assert.match(splitDisk, /<dt>Disk used<\/dt><dd>sampled peak 1 MiB across 3 gauge samples, peak reached 2026-01-02T03:04:30Z, last gauge sample 2026-01-02T03:04:35Z \(point-in-time gauge, not interval coverage\)<\/dd>/);

    const legacyDisk = render(usageSnapshot({state: "partial", as_of: "2026-01-02T03:04:35Z", measured: {...measured,
      disk: {state: "verified", read_bytes: "50", write_bytes: "3", covered_ns: "6000000000", complete: true}}}));
    assert.match(legacyDisk, /<dt>Disk read<\/dt><dd>50 B over 100% of the job window<\/dd>/);
    assert.match(legacyDisk, /<dt>Disk written<\/dt><dd>3 B \(coverage not recorded for this counter\)<\/dd>/);
    assert.doesNotMatch(legacyDisk, /Disk written<\/dt><dd>3 B over/);

    const unavailable = render(usageSnapshot({state: "unavailable", as_of: "2026-01-02T03:04:35Z", reason: "provider_origin_join_unsupported"}));
    assert.match(unavailable, /<span class="pill">Unavailable<\/span>/);
    assert.match(unavailable, /No documented source proves the runner.{0,8}s original provider allocation/);
    assert.doesNotMatch(unavailable, /mCPU/);
  } finally {
    if (server) await server.close();
    await rm(cacheDir, {recursive: true, force: true});
  }
});
