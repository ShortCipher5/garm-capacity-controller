import test from 'node:test';
import assert from 'node:assert/strict';
import { hostBudgetGuidance, assessBudget, runnerMix, RUNNER_PRESETS } from './host-budget.js';
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
