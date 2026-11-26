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
    const html = renderToStaticMarkup(React.createElement(ServerCard, { host: host(), readUsage, onManage: () => {} }));
    assert.match(html, /Backend-reported totals from last discovery/);
    assert.match(html, /These totals do not verify whole-host capacity or current headroom/);
    assert.doesNotMatch(html, /Detected physical totals/);
    assert.match(html, /Resource observations/);
    assert.match(html, /Sampling cadence and interval coverage are not supplied/);
    assert.doesNotMatch(html, /Measured utilization/);
    assert.match(html, /CPU 8,000 millicores · Memory 32,768 MiB · Disk 524,288 MiB · Runners not measured by discovery/);
    assert.match(html, /CPU fair-use cap: 3,000 millicores/);
    assert.match(html, /Ledger policy management/);
    assert.match(html, /Backend connection observation: Unverified/);
    assert.doesNotMatch(html, /<select aria-label="New host mode"/);
    const unknown = renderToStaticMarkup(React.createElement(ServerCard, { host: host({ detected: undefined, cpu_cap_millis: 0 }), readUsage }));
    assert.match(unknown, /unknown — no discovery totals recorded for this host/);
    assert.doesNotMatch(unknown, /CPU fair-use cap/);
    const stale = renderToStaticMarkup(React.createElement(ServerCard, { host: host({ observation_fresh: false }), readUsage }));
    assert.match(stale, /Stale or unavailable observation/);
    assert.doesNotMatch(stale, /Ledger policy management/);
    const view = renderToStaticMarkup(React.createElement(DiscoveryResultView, { result: discovery }));
    assert.match(view, /Discovery observation · candidate-discover/);
    assert.match(view, /Backend-reported totals \(complete\): CPU 8,000 millicores · Memory 32,768 MiB · Disk 524,288 MiB/);
    assert.match(view, /do not verify whole-host capacity, current headroom or runner limits/);
    assert.doesNotMatch(view, /Physical totals/);
    const noTotals = renderToStaticMarkup(React.createElement(DiscoveryResultView, { result: {...discovery, totals: undefined} }));
    assert.match(noTotals, /Backend totals unknown/);
    const noInventory = renderToStaticMarkup(React.createElement(DiscoveryResultView, {
      result: {...discovery, guests: [], blockers: ["guest_inventory_unavailable"]},
    }));
    assert.match(noInventory, /Guest inventory unavailable; the number of guests is unknown/);
    assert.doesNotMatch(noInventory, /No guests observed in the bounded inventory/);
    assert.doesNotMatch(noInventory, /Observed guests <small class="muted">0/);
    assert.match(noInventory, /Project guest observations: CPU 750 ms cumulative/);
    const emptyInventory = renderToStaticMarkup(React.createElement(DiscoveryResultView, {
      result: {...discovery, guests: [], utilization: undefined},
    }));
    assert.match(emptyInventory, /Observed guests <small class="muted">0/);
    assert.match(emptyInventory, /No guests observed in the bounded inventory/);
    assert.doesNotMatch(emptyInventory, /Guest inventory unavailable/);
    assert.match(view, /Workload-aware suggested budget/);
    assert.match(view, /<th scope="row">CPU<\/th><td>Not estimated<\/td><td>Not suggested<\/td>/);
    assert.doesNotMatch(view, /6,000 millicores|30,720 MiB|471,860 MiB/);
    const proposal = { version: 1, budget: {cpu_millis:4500,ram_mib:24000}, reserve:{cpu_millis:1000,ram_mib:3277,disk_mib:52429}, warnings:['Selected storage pool usage is unknown.'] };
    const withProposal = renderToStaticMarkup(React.createElement(DiscoveryResultView, { result: {...discovery,proposal} }));
    assert.match(withProposal, /CPU<\/th><td>1,000 millicores<\/td><td>4,500 millicores<\/td>/);
    assert.match(withProposal, /Memory<\/th><td>3,277 MiB<\/td><td>24,000 MiB<\/td>/);
    assert.match(withProposal, /Disk<\/th><td>52,429 MiB<\/td><td>Not suggested<\/td>/);
    assert.match(withProposal, /Selected storage pool usage is unknown/);
    assert.match(withProposal, /No disk budget was suggested\. Review the runner storage pool and discovery warnings before entering any manual budget/);
    const withDiskBudget = renderToStaticMarkup(React.createElement(DiscoveryResultView, { result: {...discovery, proposal: {...proposal, budget: {...proposal.budget, disk_mib: 40000}, warnings: []}} }));
    assert.match(withDiskBudget, /Disk<\/th><td>52,429 MiB<\/td><td>40,000 MiB<\/td>/);
    assert.doesNotMatch(withDiskBudget, /No disk budget was suggested/);
    const exhausted = renderToStaticMarkup(React.createElement(DiscoveryResultView, { result: {
      ...discovery,
      totals: {...discovery.totals, disk_mib_used: discovery.totals.disk_mib},
      guests: discovery.guests.filter(guest => guest.root_disk_mib !== undefined),
    }}));
    assert.match(exhausted, /Disk<\/th><td>52,429 MiB<\/td><td>Not suggested<\/td>/);
    assert.match(exhausted, /No disk capacity remains in this proposal/);
    assert.match(exhausted, /Review the runner storage pool and discovery warnings before entering any manual budget/);
    assert.doesNotMatch(exhausted, /enter a reviewed disk budget manually|disk usage unknown/);
    assert.doesNotMatch(withProposal, /<form|type="submit"/);
    assert.match(view, /data-unmanaged="2"/);
    assert.match(view, /runner-1/);
    assert.match(view, /Managed runner/);
    assert.match(view, />unknown<\/td>/);
    assert.match(view, /CPU 750 ms cumulative/);
    assert.match(view, /Project guest observations/);
    assert.match(view, /not whole-host load or per-job usage/);
    assert.match(view, /shared volumes may be counted more than once/);
    assert.match(view, /complete samples from the same guest counter lifetimes/);
    assert.doesNotMatch(view, /two consecutive discoveries/);
    const renderUsage = (utilization) => renderToStaticMarkup(React.createElement(DiscoveryResultView, {
      result: {...discovery, utilization},
    }));
    const partial = renderUsage({...discovery.utilization, cpu_cumulative_millis: 0, disk_used_mib: 0, errors: ["opaque"]});
    assert.match(partial, /Partial project guest observations/);
    assert.match(partial, /CPU unknown · Memory 2,560 MiB used · Disk unknown/);
    assert.match(partial, /Positive values are observed subtotals, not complete project totals/);
    assert.doesNotMatch(partial, /CPU 0 ms|Disk 0 MiB used/);
    const empty = renderUsage({source: "guest-state", cpu_cumulative_millis: 0, ram_used_mib: 0, disk_used_mib: 0, guests: 0});
    assert.match(empty, /Project guest observations: CPU 0 ms cumulative · Memory 0 MiB used · Disk 0 MiB used · 0 guest/);
    assert.doesNotMatch(empty, /Partial project guest observations/);
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
    assert.match(markup,/<button type="button" disabled="">Use planned total as runner limit<\/button>/);
    assert.match(markup,/Refresh discovery to copy a planned total/);
    assert.match(markup,/small planned count/);
    assert.match(markup,/not saved profiles/);
  } finally { await server.close(); }
});
test('planner uses saved profile snapshot without claiming counts were saved', async () => {
 const server=await createServer({server:{middlewareMode:true,hmr:false},appType:'custom'});
 try {
  const {BudgetPlanner}=await server.ssrLoadModule('/src/servers.jsx');
  const result={...discovery,proposal:{version:1,budget:{cpu_millis:6000,ram_mib:16000,disk_mib:80000},reserve:{},warnings:[],size_source:'saved-profiles',sizes:[{name:'custom-size',cpu_millis:2000,ram_mib:6144,disk_mib:40000}]}};
  const html=renderToStaticMarkup(React.createElement(BudgetPlanner,{result,budget:{...result.proposal.budget,runners:2},onChange:()=>{}}));
  assert.match(html,/Saved runner-size profiles from this discovery review/);
  assert.match(html,/custom-size planned count/);
  assert.match(html,/does not save counts/);
  assert.doesNotMatch(html,/small planned count/);
 }finally{await server.close()}
});

test("server card shows only matched runner storage health and hides stale counters", async()=>{
 const server = await createServer({server:{middlewareMode:true,hmr:false},appType:"custom"});
 try {
  const {ServerCard}=await server.ssrLoadModule("/src/servers.jsx");
  const readUsage=async()=>[];
  const now=Date.now();
  const iso=offset=>new Date(now+offset).toISOString();
  const sample={storage_pool:"fixtures",state:"current",observed_at:iso(-1000),expires_at:iso(30000),total_bytes:"123456789",available_bytes:"0",total_inodes:"2000",available_inodes:"12"};
  const show=overrides=>renderToStaticMarkup(React.createElement(ServerCard,{host:host(overrides),readUsage}));
  const current=show({runner_disk_health:[sample]});
  assert.equal((current.match(/class="usage-panel"/g) ?? []).length,1);
  assert.match(current,/class="runner-disk-health-panel" aria-label="Runner disk health"/);
  assert.match(current,/Runner disk health/);
  assert.match(current,/Storage pool fixtures/);
  assert.match(current,/0 of 123,456,789 bytes available/);
  assert.match(current,/12 of 2,000 inodes available/);
  assert.match(current,/not per-runner quotas or a capacity guarantee/);
  const stale=show({runner_disk_health:[{...sample,observed_at:iso(-30000),expires_at:iso(-1000)}]});
  assert.match(stale,/Stale: current space and inode availability are unknown/);
  assert.doesNotMatch(stale,/123,456,789|inodes available/);
  assert.match(show({}),/Unknown: no verified runner filesystem reading/);
 } finally {await server.close();}
});

test("planner distinguishes an entered override from the fresh reviewed recommendation", async () => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  try {
    const { BudgetPlanner } = await server.ssrLoadModule("/src/servers.jsx");
    const now = Date.now();
    const recommended = { cpu_millis: 2500, ram_mib: 7500, disk_mib: 50000 };
    const size = { name: "small", cpu_millis: 1000, ram_mib: 2048, disk_mib: 20480 };
    const result = {
      ...discovery,
      observed_at: new Date(now - 1000).toISOString(),
      review_expires_at: new Date(now + 60000).toISOString(),
      review: { profile_id: discovery.profile_id, id: "11111111-1111-4111-8111-111111111111", revision: "a".repeat(64) },
      proposal: { version: 1, budget: recommended, warnings: [], size_source: "saved-profiles", sizes: [size] },
    };
    const draft = { cpu_millis: 5000, ram_mib: 15000, disk_mib: 110000, runners: 0 };
    const show = (review) => renderToStaticMarkup(React.createElement(BudgetPlanner, { result: review, budget: draft, onChange: () => {} }));
    const fresh = show(result);
    assert.match(fresh, /Maximum under entered budget<\/th><th>Maximum under reviewed recommendation/);
    assert.match(fresh, /<td>5<\/td><td>2<\/td>/);
    for (const reviewed of [
      { ...recommended, cpu_millis: 0 },
      { ...recommended, ram_mib: 0 },
      { ...recommended, cpu_millis: 500 },
    ]) {
      const knownZero = { ...result, proposal: { ...result.proposal, budget: reviewed } };
      assert.match(show(knownZero), /<td>5<\/td><td>0<\/td>/);
    }
    for (const noRecommendation of [
      { ...result, observed_at: new Date(now - 60000).toISOString(), review_expires_at: new Date(now - 1000).toISOString() },
      { ...result, proposal: { ...result.proposal, budget: { ...recommended, disk_mib: undefined } } },
      { ...result, proposal: { ...result.proposal, budget: { ...recommended, disk_mib: 0 } } },
      { ...result, review: undefined },
    ]) {
      assert.match(show(noRecommendation), /<td>5<\/td><td>Unavailable<\/td>/);
    }
  } finally { await server.close(); }
});

test("resource history selects one collector at a time and keeps each scope explicit", async () => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  try {
    const { UsageHistoryDetail } = await server.ssrLoadModule("/src/servers.jsx");
    const sample = (sampled_at, cpu_used_millis, source) => ({
      host: "host-a", sampled_at, cpu_used_millis, source, ram_used_mib: 600, disk_used_mib: 900,
    });
    const samples = [
      sample("2026-01-01T00:00:00Z", 0, "guest-state"),
      sample("2026-01-01T00:01:00Z", 12, "other-source"),
      sample("2026-01-01T00:02:00Z", 0, "guest-state-v2"),
      sample("2026-01-01T00:03:00.123456789Z", 33, "guest-state-v2"),
    ];
    const render = (source, history = samples) => renderToStaticMarkup(React.createElement(
      UsageHistoryDetail, { samples: history, dimension: "cpu_used_millis", source, now: new Date("2026-01-01T00:03:45Z") },
    ));
    const latest = render(undefined);
    assert.match(latest, /Observation source/);
    assert.match(latest, /value="guest-state-v2" selected="">guest-state-v2 \(2\)/);
    assert.match(latest, /value="guest-state">guest-state \(1\)/);
    assert.match(latest, /shared devices and do not establish backing-store pressure/);
    assert.match(latest, /not whole-host load/);
    assert.match(latest, /Last sample <time datetime="2026-01-01T00:03:00\.123456789Z">45 seconds ago<\/time> · source guest-state-v2/i);
    assert.match(latest, /aria-label="2026-01-01T00:02:00Z · CPU used: 0 millicores · source guest-state-v2"/);
    assert.match(latest, /<title>2026-01-01T00:03:00.123456789Z · CPU used: 33 millicores · source guest-state-v2<\/title>/);
    assert.equal((latest.match(/<circle/g) || []).length, 2);
    assert.doesNotMatch(latest, /<polyline|stored zero in legacy|T00:00:00Z/);
    const legacy = render("guest-state");
    assert.match(legacy, /stored zero in legacy history is not proof of zero usage/);
    assert.match(legacy, /CPU used: 0 millicores · source guest-state/);
    assert.equal((legacy.match(/<circle/g) || []).length, 1);
    assert.doesNotMatch(legacy, /shared devices/);
    assert.match(render("other-source"), /Source-specific resource observations/);
    assert.equal(render("no-longer-returned"), latest);
    assert.doesNotMatch(render(undefined, [samples[0]]), /Observation source/);
    assert.equal(render(undefined, []), "");
  } finally {
    await server.close();
  }
});
