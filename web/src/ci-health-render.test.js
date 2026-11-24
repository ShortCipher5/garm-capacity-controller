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
    assert.match(panel(true, read), /Loading checks the latest available observation; it does not start a GitHub collection or automatically update the view/);
    const resources = {cpu_millis: 0, ram_mib: 0, runners: 0};
    const data = {hosts: [], pools: [], events: [], global: {budget: resources, allocated: resources}, states: {}, epoch: 0, observed_at: "2026-01-01T00:00:00Z"};
    const dashboard = (extra = {}) => renderToStaticMarkup(React.createElement(Dashboard, {data, ...extra}));
    assert.doesNotMatch(dashboard({readCIHealth: read}), /href="#ci-health"|id="ci-health"/);
    assert.doesNotMatch(dashboard({ciHealthAvailable: true}), /href="#ci-health"|id="ci-health"/);
    assert.match(dashboard({ciHealthAvailable: true, readCIHealth: read}), /href="#ci-health"/);

    const observed = "2026-01-01T00:00:00Z";
    const now = Date.parse(observed) + 5_000;
    const exact = (nanoseconds) => ({basis: "observed_endpoints", nanoseconds});
    // Synthetic timing contract only: the current GitHub reader does not
    // supply a prior queue sighting. The source-shaped fixture below omits it.
    const lower = {basis: "observed_lower_bound", nanoseconds: "7000000001"};
    const common = {source: "fixture_source", repository_id: "9007199254740993", workflow_id: "17", run_id: "9223372036854775807", observed_at: observed};
    const first = {...common, attempt: "2", status: "running", elapsed: exact("3000000000"), jobs: [
      {id: "9007199254741001", status: "queued", waiting: lower, steps: []},
      {id: "9007199254741002", status: "completed", creation_to_start: exact("1800000000000"), execution: exact("0"), steps: [
        {number: "2", status: "completed", execution: exact("2100000000")},
        {number: "7", status: "skipped"},
      ]},
    ]};
    const previous = {...common, attempt: "1", status: "completed", jobs: []};
    const snapshot = {state: "observed", observed_at: observed, timing: {attempts: [first, previous]}};
    const render = (value, at = now) => renderToStaticMarkup(React.createElement(CIHealthTiming, {snapshot: value, now: at, openRun: () => {}}));
    const html = render(snapshot);
    assert.match(html, /Active jobs in this snapshot/);
    assert.match(html, /Queued jobs \(1 observed\)/);
    assert.match(html, /Wait timing available for 1 of 1 observed jobs \(1 lower bound\)/);
    assert.match(html, /Running jobs \(0 observed\)/);
    assert.match(html, /<section aria-labelledby="ci-active-jobs-queued">.*At least 7\.000000001 s.*?<\/section>/);
    const jobControl = html.match(/aria-controls="([^"]+)" aria-label="View job 9007199254741001, run 9223372036854775807, attempt 2, workflow 17, repository 9007199254740993, source fixture_source"/)?.[1];
    assert.ok(jobControl?.endsWith("-ci-job-fixture_source:9007199254740993:17:9223372036854775807:2:9007199254741001"));
    assert.ok(html.includes(`<li id="${jobControl}" data-ci-job-row="" tabindex="-1"`));
    assert.doesNotMatch(html, /aria-label="View job 9007199254741002,/);
    assert.match(html, /Active attempts at observation/);
    assert.match(html, /Other observed attempts/);
    assert.match(html, /9223372036854775807.*attempt <code>2/);
    assert.match(html, /9223372036854775807.*attempt <code>1/);
    assert.match(html, /9007199254740993/);
    assert.match(html, /9007199254741001/);
    assert.match(html, /9007199254741002/);
    assert.match(html, /At least 7\.000000001 s/);
    assert.match(html, /Unknown/);
    assert.match(html, /title="0 nanoseconds">0 s/);
    assert.match(html, /<dt>Queue wait<\/dt><dd><span class="muted">Unknown<\/span><\/dd><\/div><div><dt>Job creation to start<\/dt><dd><span title="1800000000000 nanoseconds">30 min<\/span>/);

    // An attempt's initial status and the separate job reads are non-atomic;
    // the last job page must never stand in for another row's status time.
    const phaseAt = "2025-12-31T23:59:56.000000123Z";
    const jobAt = "2025-12-31T23:59:58.000000456Z";
    const f = {...first, status_observed_at: phaseAt, jobs: [
      {...first.jobs[0], status_observed_at: jobAt}, first.jobs[1],
    ]};
    const clocks = render({...snapshot, timing: {attempts: [f, previous]}});
    assert.match(clocks, /<dt>Status observed<\/dt><dd><time dateTime="2025-12-31T23:59:56\.000000123Z">2025-12-31 23:59:56\.000000123 UTC<\/time><\/dd>/i);
    assert.match(clocks, /<dt>Status observed<\/dt><dd><time dateTime="2025-12-31T23:59:58\.000000456Z">2025-12-31 23:59:58\.000000456 UTC<\/time><\/dd>/i);
    assert.match(clocks, /<dt>Latest included read<\/dt><dd><time dateTime="2026-01-01T00:00:00Z">2026-01-01 00:00:00 UTC<\/time><\/dd>/i);
    assert.match(clocks, /<dt>Status observed<\/dt><dd><span class="muted">Unknown<\/span><\/dd>/);
    const olderJob = {...first.jobs[1]};
    delete olderJob.creation_to_start;
    const older = render({...snapshot, timing: {attempts: [{...first, jobs: [olderJob]}, previous]}});
    assert.match(older, /<dt>Job creation to start<\/dt><dd><span class="muted">Unknown<\/span><\/dd>/);

    // Exact run navigation exists only as a verified handle: the snapshot
    // never carries a destination, and an attempt without a handle renders
    // no control rather than an inferred link.
    assert.doesNotMatch(html, /Open exact run on GitHub/);
    const runHandled = render({...snapshot, timing: {attempts: [{...first, run_link_handle: "A".repeat(43)}, previous]}});
    assert.match(runHandled, /Open exact run on GitHub/);
    assert.doesNotMatch(runHandled, /https:\/\/github\.com/);

    assert.match(html, /Step 2/);
    assert.match(html, /Step 7/);
    assert.match(html, /Not applicable \(skipped\)/);
    assert.match(html, /not the full GitHub queue or history/);
    assert.match(html, /Coverage is unknown/);
    const resourceUnavailable = /Per-job CPU, memory, and disk usage are unavailable: no documented source proves the runner.{0,8}s original provider allocation\./g;
    assert.equal([...html.matchAll(resourceUnavailable)].length, 1);
    assert.doesNotMatch(render({...snapshot, timing: {attempts: [previous]}}), resourceUnavailable);
    assert.doesNotMatch(render({...snapshot, timing: {attempts: []}}), resourceUnavailable);
    assert.match(html, /Observed attempt results by workflow/);
    const longSection = (markup) => markup.match(/<section aria-labelledby="ci-longest-steps">.*?<\/section>/)?.[0] ?? "";
    assert.match(longSection(html), /Longest measured steps in this snapshot/);
    assert.match(longSection(html), /1 of 2 observed steps have a measured completed duration/);
    assert.match(longSection(html), /Job <code>9007199254741002<\/code>, step <code>2<\/code>.*title="2100000000 nanoseconds">2\.1 s<\/span>.*Unknown/);
    const stepControl = longSection(html).match(/aria-controls="([^"]+)" aria-label="View step 2, job 9007199254741002, run 9223372036854775807, attempt 2, workflow 17, repository 9007199254740993, source fixture_source"/)?.[1];
    assert.ok(stepControl?.endsWith("-ci-step-fixture_source:9007199254740993:17:9223372036854775807:2:9007199254741002:2"));
    assert.ok(html.includes(`id="${stepControl}" data-ci-step-row="" tabindex="-1"`));
    assert.doesNotMatch(longSection(html), /step <code>7<\/code>/);
    const duplicateStep = render({...snapshot, timing: {attempts: [first,
      {...previous, jobs: [{...first.jobs[1], id: "9007199254741003"}]},
    ]}});
    const controls = [...longSection(duplicateStep).matchAll(/aria-controls="([^"]+)" aria-label="View step 2,/g)].map((m) => m[1]);
    assert.equal(controls.length, 2);
    assert.notEqual(controls[0], controls[1]);
    assert.ok(controls.some((id) => id.endsWith(":1:9007199254741003:2")));
    assert.ok(controls.some((id) => id.endsWith(":2:9007199254741002:2")));
    for (const id of controls) assert.ok(duplicateStep.includes(`id="${id}" data-ci-step-row="" tabindex="-1"`));
    assert.match(html, /Observed subset only:.*not workflow totals or outcome rates/);
    assert.match(html, /Workflow <code>17<\/code>/);
    assert.match(html, /Repository <code>9007199254740993<\/code>/);
    assert.match(html, /Source <code>fixture_source<\/code>/);
    assert.match(html, /<th scope="row">Initial<\/th><td>1<\/td><td>0<\/td><td>0<\/td><td>0<\/td><td>0<\/td><td>1<\/td>/);
    assert.match(html, /<th scope="row">Retries<\/th><td>1<\/td><td>1<\/td><td>0<\/td><td>0<\/td><td>0<\/td><td>0<\/td>/);
    assert.match(render({...snapshot, coverage: "observed_subset"}), /Only selected observations/);
    assert.match(render({...snapshot, coverage: "bounded_partial"}), /Partial coverage/);
    assert.equal(render({...snapshot, coverage: "complete"}), "");
    assert.doesNotMatch(html, /<a |<form|command|mean|p95|MiB/);
    assert.equal(render(snapshot, now + 120_000), "");
    assert.equal(render(snapshot, Date.parse(observed) - 1), "");
    assert.equal(render({state: "unavailable", timing: {attempts: []}}), "");
    assert.match(render({...snapshot, timing: {attempts: []}}), /No attempts were included/);
    assert.doesNotMatch(render({...snapshot, timing: {attempts: []}}), /Observed attempt results by workflow|Active jobs in this snapshot|Longest measured steps/);
    assert.equal(render({...snapshot, timing: {attempts: [{...first, run_id: 123}]}}), "");

    const observedFirst = structuredClone(first);
    // The eligible wait measurement is on the thirteenth row, off the table.
    const earlyStatusAt = "2025-12-31T23:59:57.000000321Z";
    const laterStatusAt = "2025-12-31T23:59:59.000000654Z";
    const queued = [
      ...Array.from({length: 10}, (_, i) => ({id: String(100 + i), status: "queued", steps: []})),
      {id: "9007199254741001", status: "queued", steps: []},
      {id: "9007199254741002", status: "queued", steps: []},
    ];
    const activeJobs = {
      ...snapshot, coverage: "bounded_partial", timing: {attempts: [
        observedFirst,
        // A GitHub requested/waiting/pending attempt projects as unknown.
        {...previous, status: "unknown", jobs: [
          ...queued.reverse(),
          {id: "9007199254741003", status: "running", steps: []},
          {id: "9007199254741004", status: "running", status_observed_at: earlyStatusAt, elapsed: exact("2000000000"), steps: []},
          {id: "9007199254741005", status: "completed", steps: []},
          {id: "9007199254741006", status: "running", status_observed_at: laterStatusAt, elapsed: exact("0"), steps: []},
        ]},
        {...common, repository_id: "9007199254740995", run_id: "9223372036854775806",
          status: "unknown", attempt: "1", jobs: [{id: "9007199254741003", status: "running", steps: []}]},
      ]},
    };
    const activeHTML = render(activeJobs);
    const sectionOf = (phase) => activeHTML.match(new RegExp('<section aria-labelledby="ci-active-jobs-' + phase + '">.*?</section>'))?.[0] ?? "";
    const queuedSection = sectionOf("queued");
    const runningSection = sectionOf("running");
    assert.match(activeHTML, /Partial coverage/);
    assert.match(activeHTML, /This is not the live queue\. Rows are ordered by identity/);
    assert.match(queuedSection, /Queued jobs \(13 observed\)/);
    assert.match(queuedSection, /Showing 12 of 13 observed jobs/);
    assert.match(queuedSection, /Wait timing available for 1 of 13 observed jobs \(1 lower bound\)/);
    assert.equal((queuedSection.match(/<th scope="row">/g) ?? []).length, 12);
    assert.deepEqual([...queuedSection.matchAll(/<th scope="row"><code>(\d+)<\/code>/g)].map((m) => m[1]),
      [...Array.from({length: 10}, (_, i) => String(100 + i)), "9007199254741001", "9007199254741002"]);
    assert.match(queuedSection, /<code>9007199254741001<\/code>.*attempt <code>1<\/code>.*Unknown/);
    assert.doesNotMatch(queuedSection, /attempt <code>2<\/code>|At least|nanoseconds/);
    assert.match(activeHTML, /<strong>Run <code>9223372036854775807<\/code>, attempt <code>2<\/code><\/strong>.*<strong>Job <code>9007199254741001<\/code><\/strong>/);
    assert.match(runningSection, /Running jobs \(4 observed\)/);
    assert.match(runningSection, /Elapsed timing available for 2 of 4 observed jobs/);
    const runningRow = (job) => [...runningSection.matchAll(/<tr>.*?<\/tr>/g)]
      .map(([row]) => row).find((row) => row.includes(`<th scope="row"><code>${job}</code>`)) ?? "";
    assert.match(runningSection, /<th scope="col">Status observed<\/th><th scope="col">Execution elapsed when observed<\/th>/);
    assert.match(runningRow("9007199254741004"), /<td><time datetime="2025-12-31T23:59:57\.000000321Z">2025-12-31 23:59:57\.000000321 UTC<\/time><\/td>/i);
    assert.match(runningRow("9007199254741006"), /<td><time datetime="2025-12-31T23:59:59\.000000654Z">2025-12-31 23:59:59\.000000654 UTC<\/time><\/td>/i);
    assert.match(runningRow("9007199254741003"), /<td><span class="muted">Unknown<\/span><\/td><td><span class="muted">Unknown<\/span><\/td>/);
    assert.doesNotMatch(runningSection, /datetime="2026-01-01T00:00:00Z"/i);
    assert.equal((queuedSection.match(/<td><span class="muted">Unknown<\/span><\/td><td><span class="muted">Unknown<\/span><\/td>/g) ?? []).length, 12);
    assert.match(runningSection, /<code>9007199254741003<\/code>.*Run <code>9223372036854775806<\/code>, attempt <code>1<\/code>.*<code>9007199254740995<\/code>.*<code>fixture_source<\/code>.*Unknown/);
    assert.equal((runningSection.match(/<th scope="row"><code>9007199254741003/g) ?? []).length, 2);
    assert.match(runningSection, /<code>9007199254741004<\/code>.*title="2000000000 nanoseconds">2 s/);
    assert.match(runningSection, /<code>9007199254741006<\/code>.*title="0 nanoseconds">0 s/);
    assert.doesNotMatch(runningSection, /9007199254741005|<a |<form/);
    assert.equal(render(activeJobs, now + 120_000), "");
    assert.equal(render({...activeJobs, timing: {attempts: [{...first, jobs: [{...first.jobs[0], id: 9007199254741001}]}]}}), "");

    const longSnapshot = {...snapshot, coverage: "bounded_partial", timing: {attempts: [
      {...previous, jobs: [{id: "9007199254741002", status: "completed", steps: [
        ...Array.from({length: 13}, (_, i) => ({number: String(i + 1), status: "completed", execution: exact(String((i + 1) * 1_000_000_000))})),
        {number: "14", status: "skipped"},
        {number: "15", status: "completed"}, // No endpoint pair.
      ]}]},
    ]}};
    const longRanking = longSection(render(longSnapshot));
    assert.match(longRanking, /13 of 15 observed steps/);
    assert.match(longRanking, /Showing 12 of 13 measured steps/);
    assert.deepEqual([...longRanking.matchAll(/, step <code>(\d+)<\/code>/g)].map((m) => m[1]),
      Array.from({length: 12}, (_, i) => String(13 - i)));
    assert.match(render(longSnapshot), /Partial coverage/);
    assert.match(render(longSnapshot), /Step 1<\/th>/); // Capped rows remain in attempt details.
    assert.equal(render(longSnapshot, Date.parse(observed) + 120_000), "");
    assert.match(longSection(render({...snapshot, timing: {attempts: [previous]}})), /No completed step durations were measured/);

    const conclusions = structuredClone(snapshot);
    conclusions.timing.attempts[0].jobs[1].conclusion = "success";
    conclusions.timing.attempts[0].jobs[1].steps[0].conclusion = "timed_out";
    conclusions.timing.attempts[0].jobs[1].steps[1].conclusion = "other_terminal";
    conclusions.timing.attempts[1].conclusion = "cancelled";
    const withResults = render(conclusions);
    assert.match(withResults, /<dt>Result<\/dt><dd><span>Success<\/span>/);
    assert.match(withResults, /<th scope="col">Status<\/th><th scope="col">Result<\/th>/);
    assert.match(withResults, /<span class="pill">Completed<\/span><\/td><td><span>Timed out<\/span>/);
    assert.match(longSection(withResults), /<td><span>Timed out<\/span><\/td>/);
    assert.match(withResults, /<span class="pill">Skipped<\/span><\/td><td><span>Other terminal result<\/span>/);
    assert.match(withResults, /<dt>Result<\/dt><dd><span>Cancelled<\/span>/);
    assert.match(withResults, /<dt>Result<\/dt><dd><span class="muted">Unknown<\/span>/);
    conclusions.timing.attempts[1].conclusion = "not-an-allowed-result";
    assert.equal(render(conclusions), "");
  } finally {
    try { await server?.close(); } finally { await rm(cacheDir, {recursive: true, force: true}); }
  }
});
