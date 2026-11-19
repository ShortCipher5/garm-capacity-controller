import {expect} from '@playwright/test';
import {test} from './fixtures.js';
test('saved-target comparison stays separate from draft edits and becomes stale on failed reload',async({page})=>{
 const repository='11111111-1111-4111-8111-111111111111',pool='22222222-2222-4222-8222-222222222222';let unavailable=false,puts=0;
 const policy={revision:2,repositories:[{id:repository,weight:1,max_runners:5,pools:[{id:pool,max_runners:4,warm_idle:1}]}]};
 const status={source:'saved-policy-and-charged-ledger',sequence:9,policy_revision:2,observed_at:'2026-01-01T00:00:00Z',ledger_ready:false,pools:[{pool,repository,desired:{...policy.repositories[0].pools[0]},mapping:'not-recorded',budget:{cpu_millis:8000,ram_mib:16000,disk_mib:200000,runners:5},charged:{cpu_millis:4000,ram_mib:8000,disk_mib:100000,runners:2}}]};
 await page.route('**/admin/api/v1/broker-policy',route=>{if(route.request().method()!=='GET'){puts++;return route.abort();}return route.fulfill(unavailable?{status:503,json:{error:'unavailable'}}:{status:200,json:{policy,status,advisory_only:true,writes_enabled:true}});});
 await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();
 const comparison=page.getByRole('region',{name:'Saved targets and charged capacity'});
 await expect(comparison.getByRole('cell',{name:'4',exact:true})).toHaveCount(1);
 await page.getByLabel('Pool runner cap',{exact:true}).fill('3');
 await expect(comparison.getByRole('cell',{name:'4',exact:true})).toHaveCount(1);
 unavailable=true;await page.getByRole('button',{name:'Reload policy (discard edits)'}).click();
 await expect(comparison).toContainText('comparison is stale');await expect(page.getByRole('button',{name:'Save capacity policy'})).toBeDisabled();
 unavailable=false;await page.getByRole('button',{name:'Reload policy (discard edits)'}).click();
 await expect(page.getByLabel('Pool runner cap',{exact:true})).toHaveValue('4');await expect(comparison).not.toContainText('comparison is stale');expect(puts).toBe(0);
});
