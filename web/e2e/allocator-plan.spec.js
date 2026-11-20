import {expect} from '@playwright/test';
import {test} from './fixtures.js';
test('fresh observed plan is read-only and unavailable refresh clears current counts',async({page})=>{
 const repository='11111111-1111-4111-8111-111111111111',pool='22222222-2222-4222-8222-222222222222';let unavailable=false,mismatchedBatch=false,mutations=0;
 const policy={revision:2,repositories:[{id:repository,weight:1,max_runners:5,pools:[{id:pool,max_runners:4,warm_idle:1}]}]};
 await page.route('**/admin/api/v1/broker-policy',route=>route.fulfill({status:200,json:{policy,advisory_only:true,writes_enabled:true,status:{source:'saved-policy-and-charged-ledger',sequence:9,policy_revision:2,observed_at:new Date().toISOString(),ledger_ready:true,pools:[{pool,repository,desired:policy.repositories[0].pools[0],mapping:'recorded'}]}}}));
 await page.route('**/admin/api/v1/allocator-plan',route=>{
  if(route.request().method()!=='GET'){mutations++;return route.abort();}
  if(unavailable)return route.fulfill({status:503,json:{state:'unavailable',reason:'pool_mapping_missing'}});
  const n=Date.now();
  const maximum=(suggested)=>({state:'proposed',host_evidence:'configured_only',expires_at:new Date(n+30000).toISOString(),
    proposals:[{pool_id:pool,repository_id:repository,observed:2,suggested,observed_min_idle:1}]});
  return route.fulfill({status:200,json:{source:'scoped-stock-and-coherent-ledger',source_identity:'a'.repeat(64),demand_digest:'b'.repeat(64),ledger_sequence:9,policy_revision:2,started_at:new Date(n-1000).toISOString(),finished_at:new Date(n-500).toISOString(),captured_at:new Date(n).toISOString(),expires_at:new Date(n+59000).toISOString(),atomic:false,remaining_jobs:1,stock_maximum_preview:maximum(3),stock_maximum_batch_preview:maximum(mismatchedBatch?1:3),pools:[{pool,repository,supply:{pool_id:pool,idle:1,busy:2,creating:0,other:1},charged:{cpu_millis:4000,ram_mib:4000,disk_mib:4000,runners:4},cap:4,warm_idle:1,additional:0,additional_warm:0,covered_by_idle:1,covered_by_grant:0,deferred_by_inflight:1}]}});
 });
 await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();
 const panel=page.getByRole('region',{name:'Observed runners and fresh allocation plan'});
 await expect(panel).toContainText('unknown until a successful fresh read');
 await expect(page.getByLabel('Pool runner cap',{exact:true})).toBeVisible();
 await panel.getByRole('button',{name:'Read fresh allocation plan'}).click();
 await expect(panel.getByRole('cell',{name:'1 / 2 / 0 / 1',exact:true})).toBeVisible();
 await expect(panel).toContainText('The preview and saved comparison report the same policy revision and ledger sequence.');
 await page.getByLabel('Pool runner cap',{exact:true}).fill('3');
 await expect(panel).toContainText('The preview and saved comparison report the same policy revision and ledger sequence.');
 await expect(panel).toContainText('saved policy revision 2');
 const batch=panel.locator('details[aria-label="Stock maximum batch comparison"]');
 await batch.getByText('Stock maximum batch comparison (read-only)',{exact:true}).click();
 await expect(batch.getByRole('cell',{name:'3',exact:true})).toBeVisible();
 await expect(batch.getByText(/does not monitor applied changes, enforce a cooldown, or queue any action/)).toBeVisible();
 await expect(batch.getByRole('button')).toHaveCount(0);
 mismatchedBatch=true;
 await panel.getByRole('button',{name:'Read fresh allocation plan'}).click();
 await expect(batch).toContainText('optional stock maximum preview is unavailable');
 if (!(await batch.evaluate(details=>details.open))) await batch.getByText('Stock maximum batch comparison (read-only)',{exact:true}).click();
 await expect(batch.getByText(/optional stock maximum preview is unavailable/)).toBeVisible();
 await expect(batch.getByRole('table')).toHaveCount(0);
 await expect(panel.getByRole('region',{name:'Stock pool maximum suggestions',exact:true}).getByRole('cell',{name:'3',exact:true})).toBeVisible();
 unavailable=true;await panel.getByRole('button',{name:'Read fresh allocation plan'}).click();
 await expect(panel).toContainText('Current observation and plan are unknown');await expect(panel).toContainText('A required enrolled pool mapping is missing.');
 await expect(panel.getByRole('cell',{name:'1 / 2 / 0 / 1',exact:true})).toHaveCount(0);
 await expect(batch).toHaveCount(0);
 expect(mutations).toBe(0);
});
