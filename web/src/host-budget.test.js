import test from 'node:test';
import assert from 'node:assert/strict';
import { hostBudgetGuidance, assessBudget, runnerMix, RUNNER_PRESETS, plannedRunnerLimit, reviewedRunnerBudget, runnerProfileMaximum } from './host-budget.js';
const result = { totals: {cpu_millis: 12000, ram_mib: 24576, disk_mib: 240000, disk_mib_used: 20000}, guests: [], blockers: [] };
test('suggestion reserves proportional headroom and existing disk usage', () => {
  const g = hostBudgetGuidance(result);
  assert.deepEqual(g.budget, {cpu_millis:10800, ram_mib:22118, disk_mib:196000});
  assert.equal(assessBudget(result, g.budget).length, 0);
  assert.match(assessBudget(result, {...g.budget, ram_mib:23552}).join(' '), /only 1024 remains/);
});
test('existing guests consume proposal budget even if attributed; missing limits are unknown', () => {
  const r = {...result, guests:[{managed:true, cpu_millis:2000, ram_mib:4096}]};
  const g = hostBudgetGuidance(r);
  assert.equal(g.budget.cpu_millis, 8800);
  assert.equal(g.budget.ram_mib, 18022);
  assert.equal(g.budget.disk_mib, undefined);
  assert.equal(hostBudgetGuidance({...result, blockers:['guest_inventory_unavailable']}).budget.ram_mib, undefined);
  assert.equal(hostBudgetGuidance({...result, totals:{...result.totals, disk_mib_used:undefined}}).budget.disk_mib, undefined);
});
test('unsafe and unconstrained budgets explain the risk', () => {
  assert.match(assessBudget(result, {cpu_millis:13000, ram_mib:23000, disk_mib:0}).join(' '), /exceeds detected/);
  assert.match(assessBudget(result, {cpu_millis:13000, ram_mib:23000, disk_mib:0}).join(' '), /unconstrained/);
});
test('mix counts combined dimensions, rather than adding independent maxima', () => {
  const budget={cpu_millis:6000,ram_mib:12288,disk_mib:122880};
  assert.deepEqual(runnerMix(budget,RUNNER_PRESETS,[0,0,0]).fits,[6,3,1]);
  assert.equal(runnerMix(budget,RUNNER_PRESETS,[2,0,1]).exceeds.length,0);
  assert.ok(runnerMix(budget,RUNNER_PRESETS,[6,3,1]).exceeds.length);
  assert.equal(runnerMix({...budget,disk_mib:''},RUNNER_PRESETS,[0,0,0]).unknown,true);
  assert.equal(runnerMix(budget,RUNNER_PRESETS,[-1,0,0]).invalid,true);
});
test('selected storage pool limits constrain disk and unavailable pool data stays unknown', () => {
  const totals={...result.totals, storage_pool:'pool',storage_pool_disk_mib:80000,storage_pool_disk_mib_used:40000};
  assert.equal(hostBudgetGuidance({...result, totals}).budget.disk_mib,29760);
  assert.equal(hostBudgetGuidance({...result, totals:{...totals,storage_pool_disk_mib_used:undefined}}).budget.disk_mib,undefined);
});
test('disk reserves host usage plus guest growth, also within the selected pool', () => {
  const r={...result,totals:{...result.totals,disk_mib:204800,disk_mib_used:102400},guests:[{cpu_millis:1000,ram_mib:2048,root_disk_mib:102400}]};
  assert.equal(hostBudgetGuidance(r).budget.disk_mib,undefined);
  const pooled={...r, totals:{...r.totals,disk_mib:1000000,disk_mib_used:1000,storage_pool:'pool',storage_pool_disk_mib:204800,storage_pool_disk_mib_used:102400}};
  assert.equal(hostBudgetGuidance(pooled).budget.disk_mib,undefined);
});

test('exhausted disk never prefills an unconstrained zero', () => {
 const g=hostBudgetGuidance({...result,totals:{...result.totals,disk_mib_used:240000}});
 assert.equal(g.budget.disk_mib,undefined);
 assert.match(g.warnings.join(' '),/No disk capacity remains/);
});
test('versioned server proposal is used verbatim rather than recomputed by browser', () => {
 const proposal={version:1,budget:{cpu_millis:1234},reserve:{cpu_millis:1000},warnings:['server review'],sizes:[],size_source:'unavailable'};
 assert.equal(hostBudgetGuidance({...result,proposal}),proposal);
});
test('unconstrained saved profile cannot contribute to a bounded mix', () => {
 const budget={cpu_millis:6000,ram_mib:12288,disk_mib:122880};
 const profiles=[{name:'unbounded',cpu_millis:1000,ram_mib:2048,disk_mib:0},RUNNER_PRESETS[0]];
 assert.deepEqual(runnerMix(budget,profiles,[0,1]).fits,[null,6]);
 assert.equal(runnerMix(budget,profiles,[1,1]).invalid,true);
});

test('disk overrides compare with the selected pool instead of claiming host remainder is pool headroom', () => {
 const scoped = {...result, totals:{...result.totals,storage_pool:'selected',storage_pool_disk_mib:80000,storage_pool_disk_mib_used:40000}};
 const g = hostBudgetGuidance(scoped);
 assert.equal(g.budget.disk_mib,29760);
 const entered = {...g.budget,disk_mib:40000};
 const over = assessBudget(scoped,entered,g).join(' ');
 assert.match(over,/10240 MiB above the suggested 29760 MiB/);
 assert.match(over,/used space.*selected storage pool/);
 assert.doesNotMatch(over,/200000 remains/);
 assert.match(assessBudget(scoped,{...entered,disk_mib:80001},g).join(' '),/selected storage pool total of 80000/);
 const exhausted = {...scoped,totals:{...scoped.totals,storage_pool_disk_mib_used:80000}};
 assert.match(assessBudget(exhausted,{...entered,disk_mib:1}).join(' '),/no budget was suggested/);
 assert.match(hostBudgetGuidance(exhausted).warnings.join(' '),/No disk capacity remains/);
});

test("copy only a positive known reviewed mix fitting both draft and suggested headroom", () => {
  const observed = "2026-01-01T00:00:00Z", expired = "2026-01-01T00:01:00Z";
  const now = Date.parse(observed) + 1000;
  const proposal = {version:1, size_source:"illustrative-presets", sizes:RUNNER_PRESETS, budget:{cpu_millis:5000,ram_mib:12000,disk_mib:110000}};
  const result = {profile_id:"candidate", review:{profile_id:"candidate",id:"11111111-1111-4111-8111-111111111111",revision:"a".repeat(64)}, observed_at:observed, review_expires_at:expired, proposal};
  const draft = {...proposal.budget,runners:0};
  assert.equal(plannedRunnerLimit(result,draft,["1","1","0"],now),2);
  assert.equal(draft.runners,0);
  assert.equal(plannedRunnerLimit(result,draft,[0,0,0],now),undefined);
  assert.equal(plannedRunnerLimit(result,draft,[0,0,2],now),undefined);
  assert.equal(plannedRunnerLimit(result,{...draft,cpu_millis:2000},[1,1,0],now),undefined);
  assert.equal(plannedRunnerLimit(result,{cpu_millis:9000,ram_mib:20000,disk_mib:200000},[0,0,2],now),undefined);
  for (const counts of [[1,0], [null,0,1], [1.5,0,0], [1,"",0], [Number.MAX_SAFE_INTEGER,1,0]]) {
    assert.equal(plannedRunnerLimit(result,draft,counts,now),undefined);
  }
  const unknown = {...result,proposal:{...proposal,budget:{cpu_millis:5000,ram_mib:12000}}};
  assert.equal(plannedRunnerLimit(unknown,draft,[1,0,0],now),undefined);
  const unbounded = {...result,proposal:{...proposal,size_source:"saved-profiles",sizes:[{...RUNNER_PRESETS[0],disk_mib:0}]}};
  assert.equal(plannedRunnerLimit(unbounded,draft,[1],now),undefined);
  assert.equal(plannedRunnerLimit(result,{...draft,disk_mib:0},[1,0,0],now),undefined);
  assert.equal(plannedRunnerLimit(result,draft,[1,0,0],Date.parse(expired)),undefined);
  assert.equal(plannedRunnerLimit(result,draft,[1,0,0],Date.parse(observed)-1),undefined);
  assert.equal(plannedRunnerLimit({...result,review_expires_at:undefined},draft,[1,0,0],now),undefined);
  assert.equal(plannedRunnerLimit({...result,review:{...result.review,profile_id:"other"}},draft,[1,0,0],now),undefined);
});

test('review-only maxima retain known zero and ignore draft count edits without enabling copying', () => {
  const now = Date.parse('2026-11-23T04:00:00Z');
  const size = RUNNER_PRESETS[0];
  const positive = { cpu_millis: 2500, ram_mib: 7500, disk_mib: 50000 };
  const input = {
    profile_id: 'synthetic',
    observed_at: new Date(now - 1000).toISOString(),
    review_expires_at: new Date(now + 1000).toISOString(),
    review: { profile_id: 'synthetic', id: '11111111-1111-4111-8111-111111111111', revision: 'a'.repeat(64) },
    proposal: { version: 1, size_source: 'saved-profiles', sizes: [size], budget: positive },
  };
  assert.equal(runnerMix(positive, [size], ['']).invalid, true);
  assert.equal(runnerProfileMaximum(reviewedRunnerBudget(input, now), size), 2);
  assert.equal(plannedRunnerLimit(input, positive, [''], now), undefined);
  assert.equal(plannedRunnerLimit(input, positive, [2], now), 2);
  assert.equal(plannedRunnerLimit(input, {...positive, cpu_millis: 1000}, [2], now), undefined);
  for (const key of ['cpu_millis', 'ram_mib']) {
    const zero = {...input, proposal: {...input.proposal, budget: {...positive, [key]: 0}}};
    assert.equal(runnerProfileMaximum(reviewedRunnerBudget(zero, now), size), 0);
    assert.equal(plannedRunnerLimit(zero, positive, [1], now), undefined);
  }
  assert.equal(runnerProfileMaximum({...positive, cpu_millis: 500}, size), 0);
  assert.equal(runnerProfileMaximum(reviewedRunnerBudget(input, now + 1000), size), null);
  assert.equal(runnerProfileMaximum({...positive, disk_mib: 0}, size), null);
  assert.equal(runnerProfileMaximum({...positive, cpu_millis: null}, size), null);
  assert.equal(runnerProfileMaximum(positive, {...size, disk_mib: 0}), null);
});
