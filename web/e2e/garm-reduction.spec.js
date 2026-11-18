import {expect} from '@playwright/test';import {test} from './fixtures.js';
test('managed mode exposes maximum-only review and no generic write controls',async({page})=>{
 let posts=0;
 const pool={id:'00000000-0000-4000-8000-000000000001',provider_name:'capacity',fingerprint:'f'.repeat(64),enabled:true,priority:0,max_runners:4,min_idle_runners:1,generation:1,tags:['linux']};
 await page.route('**/admin/api/v1/garm',route=>route.fulfill({status:200,json:{maximum_reduction_only:true,settings_available:true,writes_enabled:true,write_blocked:false,history:[],history_truncated:false,settings:{pools:[pool],repositories:[],writes_enabled:true}}}));
 await page.route('**/admin/api/v1/garm/pool',route=>{posts++;const body=route.request().postDataJSON();expect(body.pool).toEqual({max_runners:3});pool.max_runners=3;return route.fulfill({status:200,json:{recorded_confirmed:true}});});
 await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();await page.getByRole('button',{name:'Load Garm settings'}).click();
 await expect(page.getByText('Managed single-writer: maximum reductions only.',{exact:true})).toBeVisible();
 for(const name of ['Pool enabled','Pool priority','Minimum idle runners','Future-runner tags, one per line','Repository balancing'])await expect(page.getByLabel(name,{exact:true})).toHaveCount(0);
 await page.getByLabel('Maximum runners',{exact:true}).fill('3');await page.getByLabel('Garm audit reason').fill('reduce idle ceiling');await page.getByRole('button',{name:'Review Garm change'}).click();await expect(page.getByText('max_runners: 3',{exact:true})).toBeVisible();await page.getByLabel('Garm management write token').fill('b'.repeat(64));await page.getByRole('button',{name:'Apply reviewed Garm change'}).click();await expect(page.getByText('Operation recorded as confirmed.',{exact:false})).toBeVisible();expect(posts).toBe(1);
});
