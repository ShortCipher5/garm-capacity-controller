import test from "node:test";
import assert from "node:assert/strict";
import {mkdtemp, rm} from "node:fs/promises";
import {tmpdir} from "node:os";
import {join} from "node:path";
import React from "react";
import {renderToStaticMarkup} from "react-dom/server";
import {createServer} from "vite";

test("native timing shows only verified evidence and explicitly guarded navigation", async () => {
  const cacheDir = await mkdtemp(join(tmpdir(), "garm-ci-health-render-"));
  let server;
  try {
    server = await createServer({cacheDir, server: {middlewareMode: true, hmr: false}, appType: "custom"});
    const {CIHealthPanel, CIHealthTiming} = await server.ssrLoadModule("/src/ci-health.jsx");
    const {Dashboard} = await server.ssrLoadModule("/src/main.jsx");
    const read = () => { assert.fail("render must not probe the optional route"); };
    const panel = (enabled, reader) => renderToStaticMarkup(React.createElement(CIHealthPanel, {enabled, read: reader}));
    assert.equal(panel(undefined, read), "");
    assert.equal(panel("true", read), "");
    assert.equal(panel(true, undefined), "");
    assert.match(panel(true, read), /CI Health.*Load timing snapshot/);
    const resources = {cpu_millis: 0, ram_mib: 0, runners: 0};
    const data = {hosts: [], pools: [], events: [], global: {budget: resources, allocated: resources}, states: {}, epoch: 0, observed_at: "2026-01-01T00:00:00Z"};
    const dashboard = (extra = {}) => renderToStaticMarkup(React.createElement(Dashboard, {data, ...extra}));
    assert.doesNotMatch(dashboard({readCIHealth: read}), /href="#ci-health"|id="ci-health"/);
    assert.doesNotMatch(dashboard({ciHealthAvailable: true}), /href="#ci-health"|id="ci-health"/);
    assert.match(dashboard({ciHealthAvailable: true, readCIHealth: read}), /href="#ci-health"/);

    const observed = "2026-01-01T00:00:00Z";
    const now = Date.parse(observed) + 5_000;
    const exact = (nanoseconds) => ({basis: "observed_endpoints", nanoseconds});
    const lower = {basis: "observed_lower_bound", nanoseconds: "7000000001"};
    const common = {source: "fixture_source", repository_id: "9007199254740993", workflow_id: "17", run_id: "9223372036854775807", observed_at: observed};
    const first = {...common, attempt: "2", status: "running", elapsed: exact("3000000000"), jobs: [
      {id: "9007199254741001", status: "queued", waiting: lower, steps: []},
      {id: "9007199254741002", status: "completed", execution: exact("0"), steps: [
        {number: "2", status: "completed", execution: exact("2100000000")},
        {number: "7", status: "skipped"},
      ]},
    ]};
    const previous = {...common, attempt: "1", status: "completed", jobs: []};
    const snapshot = {state: "observed", observed_at: observed, timing: {attempts: [first, previous]}};
    const render = (value, at = now) => renderToStaticMarkup(React.createElement(CIHealthTiming, {snapshot: value, now: at}));
    const html = render(snapshot);
    assert.match(html, /Active at observation/);
    assert.match(html, /Other observed attempts/);
    assert.match(html, /9223372036854775807.*attempt <code>2/);
    assert.match(html, /9223372036854775807.*attempt <code>1/);
    assert.match(html, /9007199254740993/);
    assert.match(html, /9007199254741001/);
    assert.match(html, /9007199254741002/);
    assert.match(html, /At least 7\.000000001 s/);
    assert.match(html, /Unknown/);
    assert.match(html, /title="0 nanoseconds">0 s/);
    assert.match(html, /Step 2/);
    assert.match(html, /Step 7/);
    assert.match(html, /Not applicable \(skipped\)/);
    assert.match(html, /not the full GitHub queue or history/);
    assert.match(html, /Coverage is unknown/);
    assert.match(render({...snapshot, coverage: "observed_subset"}), /Only selected observations/);
    assert.match(render({...snapshot, coverage: "bounded_partial"}), /Partial coverage/);
    assert.equal(render({...snapshot, coverage: "complete"}), "");
    assert.doesNotMatch(html, /<a |<form|command|mean|p95|MiB/);
    assert.equal(render(snapshot, now + 120_000), "");
    assert.equal(render(snapshot, Date.parse(observed) - 1), "");
    assert.equal(render({state: "unavailable", timing: {attempts: []}}), "");
    assert.match(render({...snapshot, timing: {attempts: []}}), /No attempts were included/);
    assert.equal(render({...snapshot, timing: {attempts: [{...first, run_id: 123}]}}), "");
  } finally {
    try { await server?.close(); } finally { await rm(cacheDir, {recursive: true, force: true}); }
  }
});
