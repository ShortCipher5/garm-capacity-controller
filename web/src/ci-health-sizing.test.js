import test from "node:test";
import assert from "node:assert/strict";
import {mkdtemp, rm} from "node:fs/promises";
import {tmpdir} from "node:os";
import {join} from "node:path";
import React from "react";
import {renderToStaticMarkup} from "react-dom/server";
import {createServer} from "vite";

const at = "2026-01-01T00:00:00Z";
function sizingSnapshot(overrides = {}) {
  const sizing = {
    policy_version: "job-sizing-cohort-policy/v1", advisory_version: "job-sizing-advisory/v1",
    outcome: "increase_advised",
    scope: {source: "registry_1", repository_id: "9007199254740993", workflow_id: "17"},
    identity: {matrix: "ubuntu-24.04", profile: "small", revision: "rev-1"},
    window: {from: "2025-12-25T00:00:00Z", as_of: at, latest_completion_max_age_ns: "3600000000000"},
    freshness: {latest_primary_completion: "2025-12-31T23:00:00Z", age_ns: "3600000000000", fresh: true},
    coverage: "complete",
    samples: {eligible: 20, primary: 20, retried: 0, successful: 20, at_risk: 0, unknown: 0, unsuccessful: 0, throttled: 0, oom: 0, disk_full: 0, external_builder: 0, qualifying_cpu: 20, qualifying_ram: 20},
    observed: {cpu_peak_milli: "900", ram_peak_bytes: "96", cpu_saturated: 20, ram_saturated: 0},
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

test("advisory runner sizing renders only qualified advice and never an apply action", async () => {
  const cacheDir = await mkdtemp(join(tmpdir(), "garm-ci-sizing-render-"));
  let server;
  try {
    server = await createServer({cacheDir, server: {middlewareMode: true, hmr: false}, appType: "custom"});
    const {CISizingPanel, CISizingAdvisory} = await server.ssrLoadModule("/src/ci-health-sizing.jsx");
    const {Dashboard} = await server.ssrLoadModule("/src/main.jsx");
    const read = () => { assert.fail("render must not probe the optional route"); };
    const panel = (enabled, reader) => renderToStaticMarkup(React.createElement(CISizingPanel, {enabled, read: reader}));
    assert.equal(panel(undefined, read), "");
    assert.equal(panel("true", read), "");
    assert.equal(panel(true, undefined), "");
    assert.match(panel(true, read), /Runner sizing \(advisory\).*Load sizing snapshot/s);
    assert.match(panel(true, read), /leaves every scaling setting unchanged, has no apply action/);

    const resources = {cpu_millis: 0, ram_mib: 0, runners: 0};
    const data = {hosts: [], pools: [], events: [], global: {budget: resources, allocated: resources}, states: {}, epoch: 0, observed_at: at};
    const dashboard = (extra = {}) => renderToStaticMarkup(React.createElement(Dashboard, {data, ...extra}));
    assert.doesNotMatch(dashboard({readCISizing: read}), /href="#ci-health-sizing"|id="ci-health-sizing"/);
    assert.doesNotMatch(dashboard({ciHealthSizingAvailable: true}), /href="#ci-health-sizing"|id="ci-health-sizing"/);
    assert.match(dashboard({ciHealthSizingAvailable: true, readCISizing: read}), /href="#ci-health-sizing"/);

    const render = (value) => renderToStaticMarkup(React.createElement(CISizingAdvisory, {snapshot: value}));
    const html = render(sizingSnapshot());
    assert.match(html, /Reviewed through:<\/strong> <time/);
    assert.match(html, /matrix <code>ubuntu-24\.04<\/code>/);
    assert.match(html, /Distinct matrix cohorts and changed runner profiles are never combined\./);
    assert.match(html, /Eligible samples.*20 \(20 successful primary, 0 retried\)/s);
    assert.match(html, /<code>900<\/code> millicores \(20 near-saturated samples\)/);
    assert.match(html, /a sampled peak is not a complete upper bound/);
    assert.match(html, /CPU<\/dt>\s*<dd>\s*<strong>Increase advised<\/strong> — suggested <code>1125<\/code> millicores/);
    assert.match(html, /Memory<\/dt>\s*<dd>\s*<strong>No change advised<\/strong>/);
    assert.match(html, /Caller-supplied display context for host <code>host-a<\/code>/);
    assert.match(html, /The reviewed profile and advice fit within the supplied host budget context\./);
    assert.match(html, /Sustained CPU pressure/);
    assert.match(html, /Disk advice unavailable: no independently attributable disk data supports it\./);
    assert.match(html, /2 in-scope candidates were incomparable \(different matrix, profile or revision\) and were not combined\./);
    assert.match(html, /Rejected: <span>1 outside the review window \(stale\)<\/span>/);
    assert.doesNotMatch(html, /<form|Apply|apply change/i);

    // A hard fit failure is visible as a failure, never converted to a warning.
    const failure = render(sizingSnapshot({
      outcome: "needs_manual_review",
      cpu: {state: "needs_manual_review", suggested: null, reason: "change_exceeds_reviewed_bound"},
      warnings: ["insufficient_spare_capacity"],
      host: {known: true, host: "host-a", cpu_budget_milli: "64000", cpu_allocated_milli: "63900", cpu_headroom_milli: "100", ram_budget_bytes: "68719476736", ram_allocated_bytes: "4294967296", ram_headroom_bytes: "64424509440", fit: "does_not_fit"},
    }));
    assert.match(failure, /role="alert".*Does not fit the reviewed host budget context\..*hard fit failure, not a warning/s);
    assert.match(failure, /Insufficient spare capacity/);
    assert.match(failure, /The implied change exceeds the reviewed advisory bounds\./);
    assert.doesNotMatch(failure, /suggested <code>/);

    // Neutral no-recommendation stays neutral and distinguishes missing from low.
    const neutral = render(sizingSnapshot({
      outcome: "insufficient_history",
      observed: {cpu_peak_milli: null, ram_peak_bytes: null, cpu_saturated: 0, ram_saturated: 0},
      uncertainty: "",
      cpu: {state: "insufficient_history", suggested: null, reason: "insufficient_primary_attempts"},
      ram: {state: "insufficient_history", suggested: null, reason: "insufficient_primary_attempts"},
      warnings: [],
      host: {known: false, host: "", cpu_budget_milli: null, cpu_allocated_milli: null, cpu_headroom_milli: null, ram_budget_bytes: null, ram_allocated_bytes: null, ram_headroom_bytes: null, fit: "unknown"},
    }));
    assert.match(neutral, /<strong>Insufficient history<\/strong>/);
    assert.match(neutral, /Too few independent primary attempts\./);
    assert.match(neutral, /Unknown \(no qualifying measurement\)/);
    assert.match(neutral, /Host budget and headroom context unavailable; fit cannot be assessed\./);

    const lowUsage = render(sizingSnapshot({
      outcome: "unchanged",
      observed: {cpu_peak_milli: "100", ram_peak_bytes: "10", cpu_saturated: 0, ram_saturated: 0},
      cpu: {state: "unchanged", suggested: null, reason: ""},
      warnings: [],
    }));
    assert.match(lowUsage, /<code>100<\/code> millicores/);
    assert.match(lowUsage, /Reductions are never advised automatically\./);

    const disk = render(sizingSnapshot({
      outcome: "unchanged",
      cpu: {state: "unchanged", suggested: null, reason: ""},
      disk: {state: "observed", peak_bytes: "1119", samples: 18, qualified: 20},
    }));
    assert.match(disk, /Observed disk usage peak <code>1119<\/code> bytes across 18 of 20 qualifying samples/);
  } finally {
    if (server) await server.close();
    await rm(cacheDir, {recursive: true, force: true});
  }
});
