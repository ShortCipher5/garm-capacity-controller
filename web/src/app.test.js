import assert from "node:assert/strict";
import test from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";

test("initial application renders login without an authenticated snapshot", async () => {
  const server = await createServer({
    server: { middlewareMode: true, hmr: false },
    appType: "custom",
  });
  try {
    // Exercise the real JSX entry component, not a separate login fixture. A
    // snapshot dereference while App's initial data is null must fail here.
    const { App, Dashboard } = await server.ssrLoadModule("/src/main.jsx");
    const html = renderToStaticMarkup(React.createElement(App));
    assert.match(html, /Connect to your ledger/);
    assert.match(html, /Admin read token/);
    assert.match(html, /Connect read-only/);
    assert.doesNotMatch(html, /Historical execution plans unavailable/);
    assert.doesNotMatch(html, /Original source preparation history unavailable/);
    assert.doesNotMatch(html, /Allocation overview/);
    assert.doesNotMatch(html, /Stock settings apply/);

    // The fix must not remove the warning from authenticated snapshots.
    const resources = { cpu_millis: 0, ram_mib: 0, runners: 0 };
    const data = {
      legacy_unplanned_reservations: 2, legacy_source_preparations:3,
      management_enabled: false,
      garm_management_available: false,
      observed_at: "2026-01-01T00:00:00Z",
      epoch: 0,
      global: { budget: resources, allocated: resources },
      hosts: [],
      pools: [],
      states: {},
      events: [],
    };
    const dashboard = renderToStaticMarkup(
      React.createElement(Dashboard, { data }),
    );
    const placement = renderToStaticMarkup(React.createElement(Dashboard, { data: { ...data, pools: [{id:"pool-a", hosts:["missing-host"], budget:resources, allocated:resources}] } }));
    assert.match(placement, /0 host gates not paused · 0 paused · 1 unavailable in this snapshot/);
    assert.match(placement, /not live health, available budget, pool admission or a permission to provision/);
    const stockDiagnostics = dashboard.match(/<section[^>]*aria-label="Stock settings apply controller state"[^>]*>.*?<\/section>/)?.[0];
    assert.ok(stockDiagnostics, "stock diagnostics render on the authenticated dashboard");
    assert.match(stockDiagnostics, /no qualified read-only source for operation status/);
    assert.match(stockDiagnostics, /Recorded runner charge: unavailable here/);
    assert.match(stockDiagnostics, /Local maintenance pause status: unavailable here/);
    assert.match(stockDiagnostics, /Ready to resume apply work: no/);
    assert.doesNotMatch(stockDiagnostics, /<button|<form|<input|Last recorded runner charge: 0/);
    assert.match(dashboard, /Historical execution plans unavailable/);
    assert.match(dashboard, /<label for="host-attention">Show hosts<\/label>/);
    assert.match(dashboard, /Showing 0 of 0 hosts in this snapshot/);
    assert.match(dashboard, /No hosts configured in this ledger/);
    assert.match(dashboard, /<label for="audit-event-kind">Event kind<\/label><select id="audit-event-kind"/);
    assert.match(dashboard, /<label for="audit-event-host">Event host<\/label><select id="audit-event-host"/);
    assert.match(dashboard, /Showing 0 of 0 events in this snapshot/);
    assert.match(dashboard, /No audit events recorded/);
    assert.match(dashboard, /2 reservations remain charged/);
    assert.match(dashboard, /Original source preparation history unavailable/);
    assert.match(dashboard, /3 historical reservations remain charged/);
    assert.match(dashboard, /cannot reset or clear their history/);
  } finally {
    await server.close();
  }
});
