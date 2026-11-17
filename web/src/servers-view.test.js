import { test } from "node:test";
import assert from "node:assert/strict";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";

const host = (overrides = {}) => ({
  id: "host-a",
  mode: "enabled",
  new_claims_paused: false,
  observation_fresh: true,
  last_observed_healthy: true,
  budget: { cpu_millis: 4000, ram_mib: 8192, runners: 4 },
  allocated: { cpu_millis: 1000, ram_mib: 1024, runners: 1 },
  detected: { cpu_millis: 8000, ram_mib: 32768, disk_mib: 524288, runners: 0 },
  cpu_cap_millis: 3000,
  ...overrides,
});
const discovery = {
  profile_id: "candidate-discover",
  observed_at: "2026-01-01T00:00:00Z",
  identity: { observation_available: false, stored_binding_class: "not_checked" },
  totals: { complete: true, cpu_millis: 8000, ram_mib: 32768, disk_mib: 524288, disk_mib_used: 131072 },
  guests: [
    { name: "runner-1", status: "Running", managed: true, controller: "ctl-1", pool: "pool-a", reservation: "res-1", cpu_millis: 2000, ram_mib: 4096, root_disk_mib: 20480 },
    { name: "sidecar", status: "Running", managed: false, cpu_millis: 500, ram_mib: 1024, root_disk_mib: 5120 },
    { name: "opaque", status: "Stopped", managed: false },
  ],
  utilization: { source: "guest-state", cpu_cumulative_millis: 750, ram_used_mib: 2560, disk_used_mib: 25600, guests: 2 },
  suggested_budget: { cpu_millis: 6000, ram_mib: 30720, disk_mib: 471860, runners: 0 },
  warnings: ["point-in-time snapshot: observations go stale immediately and are not reconciliation evidence"],
  blockers: ["physical_capacity_scope_unqualified"],
};
test("server cards render detected totals, cpu cap and link mode controls without duplicating them", async () => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  try {
    const { ServerCard, DiscoveryResultView } = await server.ssrLoadModule("/src/servers.jsx");
    const readUsage = async () => [];
    const html = renderToStaticMarkup(React.createElement(ServerCard, { host: host(), readUsage }));
    assert.match(html, /Detected physical totals/);
    assert.match(html, /CPU 8,000 millicores · Memory 32,768 MiB · Disk 524,288 MiB · Runners not measured by discovery/);
    assert.match(html, /CPU fair-use cap: 3,000 millicores/);
    assert.match(html, /Ledger policy management/);
    assert.doesNotMatch(html, /<select aria-label="New host mode"/);
    const unknown = renderToStaticMarkup(React.createElement(ServerCard, { host: host({ detected: undefined, cpu_cap_millis: 0 }), readUsage }));
    assert.match(unknown, /unknown — no discovery totals recorded for this host/);
    assert.doesNotMatch(unknown, /CPU fair-use cap/);
    const stale = renderToStaticMarkup(React.createElement(ServerCard, { host: host({ observation_fresh: false }), readUsage }));
    assert.match(stale, /Stale or unavailable observation/);
    const view = renderToStaticMarkup(React.createElement(DiscoveryResultView, { result: discovery }));
    assert.match(view, /Discovery observation · candidate-discover/);
    assert.match(view, /CPU 6,000 millicores · Memory 30,720 MiB · Disk 471,860 MiB · Runners 0/);
    assert.match(view, /data-unmanaged="2"/);
    assert.match(view, /runner-1/);
    assert.match(view, /Managed runner/);
    assert.match(view, />unknown<\/td>/);
    assert.match(view, /CPU 750 ms cumulative/);
    assert.match(view, /physical_capacity_scope_unqualified/);
    assert.match(view, /point-in-time snapshot/);
    assert.match(view, /Suggested budget — advisory only, never applied/);
  } finally {
    await server.close();
  }
});

test('enrollment planner presents headroom warnings, distinct alternatives and unknown mix', async () => {
  const server = await createServer({ server: { middlewareMode: true }, appType: 'custom' });
  try {
    const { BudgetPlanner } = await server.ssrLoadModule('/src/servers.jsx');
    const markup = renderToStaticMarkup(React.createElement(BudgetPlanner, {result:discovery, budget:{cpu_millis:8000,ram_mib:32000,disk_mib:0,runners:0},onChange:()=>{}}));
    assert.match(markup,/Budget needs review/);
    assert.match(markup,/unconstrained disk/);
    assert.match(markup,/Maximum|maximum/);
    assert.match(markup,/Mix unavailable/);
    assert.match(markup,/small planned count/);
    assert.match(markup,/not saved profiles/);
  } finally { await server.close(); }
});
