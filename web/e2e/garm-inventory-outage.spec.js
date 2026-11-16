import { expect } from '@playwright/test';
import { test } from './fixtures.js';

test('failed background refresh withdraws old collections and reconnect restores observations without losing draft',async({page})=>{
 let fail=false, version=1, posts=0;
 const pool={id:'00000000-0000-4000-8000-000000000001',provider_name:'capacity',fingerprint:'f'.repeat(64),enabled:true,priority:0,max_runners:4,min_idle_runners:1,generation:1,tags:['linux']};
 await page.route('**/admin/api/v1/garm',route=>{
  if(route.request().method()!=='GET'){posts++;return route.abort();}
  if(fail)return route.fulfill({status:503,json:{error:'unavailable'}});
  const value={settings_available:true,writes_enabled:true,write_blocked:false,history:[],history_truncated:false,settings:{pools:[pool],repositories:[],writes_enabled:true},pool_inventory:{pools:[{...pool,fingerprint:'',configured:true,discovery_fingerprint:'b'.repeat(64)}],missing_configured_pool_ids:[]},scaleset_inventory:{scalesets:[{id:version,name:`set-version-${version}`,provider_name:'capacity',owner_kind:'unknown',state:'created',enabled:true,max_runners:4,min_idle_runners:1,generation:1,tags:[],discovery_fingerprint:'b'.repeat(64)}]}};
  return route.fulfill({status:200,json:value});
 });
 await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();
 await page.getByRole('button',{name:'Load Garm settings'}).click();await expect(page.getByRole('heading',{name:'set-version-1'})).toBeVisible();
 await page.getByLabel('Pool priority').fill('19');await page.getByLabel('Garm audit reason').fill('preserve outage draft');
 fail=true;await page.evaluate(()=>window.dispatchEvent(new Event('focus')));
 await expect(page.getByText('pool and scaleset inventories are unavailable.',{exact:false})).toBeVisible();
 await expect(page.getByRole('heading',{name:'set-version-1'})).toHaveCount(0);
 await expect(page.getByText('Existing scaleset inventory is unavailable or unsupported.',{exact:false})).toBeVisible();
 await expect(page.getByLabel('Pool priority')).toHaveValue('19');await expect(page.getByLabel('Garm audit reason')).toHaveValue('preserve outage draft');await expect(page.getByRole('button',{name:'Review Garm change'})).toBeDisabled();
 fail=false;version=2;await page.evaluate(()=>window.dispatchEvent(new Event('focus')));
 await expect(page.getByRole('heading',{name:'set-version-2'})).toBeVisible();await expect(page.getByLabel('Pool priority')).toHaveValue('19');await expect(page.getByLabel('Garm audit reason')).toHaveValue('preserve outage draft');expect(posts).toBe(0);
});
