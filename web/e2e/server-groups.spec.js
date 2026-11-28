import {expect} from '@playwright/test';
import {test} from './fixtures.js';
test('operator creates and reloads a named group in the capacity policy',async({page})=>{
 const repo='11111111-1111-4111-8111-111111111111';
 let policy={revision:0,repositories:[{id:repo,weight:1,max_runners:2,pools:[]}]};let writes=0;
 await page.route('**/admin/api/v1/broker-policy',route=>{
   if(route.request().method()==='PUT'){
     const payload=route.request().postDataJSON();expect(payload.groups[0]).toMatchObject({name:'Build',hosts:['host-a'],repositories:[repo],limit:{cpu_millis:2000,ram_mib:2048,runners:2}});
     writes++;policy={...payload,revision:1};return route.fulfill({json:{saved:true,advisory_only:true,revision:1}});
   }
   return route.fulfill({json:{policy,advisory_only:true,writes_enabled:true,eligible_hosts:['host-a'],group_charges:Object.fromEntries((policy.groups||[]).map(g=>[g.id,{cpu_millis:1000,ram_mib:512,runners:1}]))}});
 });
 await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();
 const panel=page.locator('#broker-policy');await panel.getByRole('button',{name:'Add server group'}).click();
 await panel.getByLabel('Group name').fill('Build');await panel.getByLabel('CPU ceiling (millicores)').fill('2000');await panel.getByLabel('RAM ceiling (MiB)').fill('2048');await panel.getByLabel('Runner slot ceiling').fill('2');
 await panel.getByLabel('Server host-a').check();await panel.getByLabel('All pools of '+repo).check();await panel.getByLabel('Separate management token').fill('c'.repeat(64));await panel.getByRole('button',{name:'Save capacity policy'}).click();
 await expect(panel.getByRole('status')).toContainText('saved');await panel.getByRole('button',{name:'Reload policy (discard edits)'}).click();
 await expect(panel.getByText(/Build: 1000\/2000 millicores, 512\/2048 MiB, 1\/2 slots/)).toBeVisible();expect(writes).toBe(1);
});
