import {expect} from '@playwright/test';
import {test} from './fixtures.js';
test('editing CPU retains an existing disk ceiling in every budget scope',async({page})=>{
 // Capture the fixture through Chromium's pinned TLS session. route.fetch
 // uses Node TLS and does not inherit the fixture's exact SPKI trust.
 const initial=page.waitForResponse(response=>response.url().endsWith('/admin/api/v1/overview')&&response.request().method()==='GET');
 await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();
 const body=await (await initial).json();
 body.global.budget.disk_mib=90000;
 for(const host of body.hosts)host.budget.disk_mib=60000;
 for(const pool of body.pools)pool.budget.disk_mib=30000;
 await page.route('**/admin/api/v1/overview',route=>route.fulfill({status:200,json:body}));
 await page.getByRole('button',{name:'Refresh snapshot',exact:true}).click();
 const writes=[];await page.route('**/admin/api/v1/policy',async route=>{writes.push(route.request().postDataJSON());await route.fulfill({status:200,json:{applied:true}});});

 for(const [scope,disk] of [['global_budget',90000],['host_budget',60000],['pool_budget',30000]]){
  await page.getByLabel('Policy control').selectOption(scope);
  await expect(page.getByLabel('Budget disk MiB')).toHaveValue(String(disk));
  await page.getByLabel('Budget CPU millicores').fill('3000');
  await page.getByLabel('Audit reason',{exact:true}).fill('preserve disk while editing CPU');
  await page.getByRole('button',{name:'Review change',exact:true}).click();
  await expect(page.locator('#management')).toContainText(`${disk} MiB disk`);
  await page.getByRole('button',{name:'Cancel review',exact:true}).click();
  await expect(page.getByLabel('Budget disk MiB')).toHaveValue(String(disk));
  await page.getByLabel('Budget CPU millicores').fill('3000');
  await page.getByLabel('Audit reason',{exact:true}).fill('preserve disk while editing CPU');
  await page.getByRole('button',{name:'Review change',exact:true}).click();
  await page.getByLabel('Management write token',{exact:true}).fill('c'.repeat(64));
  await page.getByRole('button',{name:'Apply reviewed change',exact:true}).click();
  await expect.poll(()=>writes.length).toBe([90000,60000,30000].indexOf(disk)+1);
  expect(writes.at(-1).budget.disk_mib).toBe(disk);expect(Object.keys(writes.at(-1).budget).sort()).toEqual(['cpu_millis','disk_mib','ram_mib','runners']);
 }
});
