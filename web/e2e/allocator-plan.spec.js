import {expect} from '@playwright/test';
import {test} from './fixtures.js';
test('fresh observed plan is read-only and unavailable refresh clears current counts',async({page})=>{
 const repository='11111111-1111-4111-8111-111111111111',pool='22222222-2222-4222-8222-222222222222';let unavailable=false,mutations=0;
 const policy={revision:2,repositories:[{id:repository,weight:1,max_runners:5,pools:[{id:pool,max_runners:4,warm_idle:1}]}]};
 await page.route('**/admin/api/v1/broker-policy',route=>route.fulfill({status:200,json:{policy,advisory_only:true,writes_enabled:true}}));
 await page.route('**/admin/api/v1/allocator-plan',route=>{
  if(route.request().method()!=='GET'){mutations++;return route.abort();}
  if(unavailable)return route.fulfill({status:503,json:{state:'unavailable',reason:'pool_mapping_missing'}});
  const n=Date.now();
  return route.fulfill({status:200,json:{source:'scoped-stock-and-coherent-ledger',source_identity:'a'.repeat(64),demand_digest:'b'.repeat(64),ledger_sequence:9,policy_revision:2,started_at:new Date(n-1000).toISOString(),finished_at:new Date(n-500).toISOString(),captured_at:new Date(n).toISOString(),expires_at:new Date(n+59000).toISOString(),atomic:false,remaining_jobs:1,pools:[{pool,repository,supply:{pool_id:pool,idle:1,busy:2,creating:0,other:1},charged:{cpu_millis:4000,ram_mib:4000,disk_mib:4000,runners:4},cap:4,warm_idle:1,additional:0,additional_warm:0,covered_by_idle:1,covered_by_grant:0,deferred_by_inflight:1}]}});
 });
 await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();
 const panel=page.getByRole('region',{name:'Observed runners and fresh allocation plan'});
 await expect(panel).toContainText('unknown until a successful fresh read');
 await panel.getByRole('button',{name:'Read fresh allocation plan'}).click();
 await expect(panel.getByRole('cell',{name:'1 / 2 / 0 / 1',exact:true})).toBeVisible();
 await expect(panel).toContainText('Fresh captured comparison');
 await page.getByLabel('Pool runner cap',{exact:true}).fill('3');
 await expect(panel).toContainText('saved policy revision 2');
 unavailable=true;await panel.getByRole('button',{name:'Read fresh allocation plan'}).click();
 await expect(panel).toContainText('Current observation and plan are unknown');await expect(panel).toContainText('A required enrolled pool mapping is missing.');
 await expect(panel.getByRole('cell',{name:'1 / 2 / 0 / 1',exact:true})).toHaveCount(0);
 expect(mutations).toBe(0);
});
