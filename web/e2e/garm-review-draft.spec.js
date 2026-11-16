import { expect } from '@playwright/test';
import { test } from './fixtures.js';

test('cancel review preserves unsent pool edits and audit reason without a write', async ({page})=>{
 let posts=0;
 const pool={id:'00000000-0000-4000-8000-000000000001',provider_name:'capacity',fingerprint:'f'.repeat(64),enabled:true,priority:0,max_runners:4,min_idle_runners:1,generation:1,tags:['linux']};
 await page.route('**/admin/api/v1/garm',route=>route.fulfill({status:200,json:{settings_available:true,writes_enabled:true,write_blocked:false,history:[],history_truncated:false,settings:{pools:[pool],repositories:[],writes_enabled:true}}}));
 await page.route('**/admin/api/v1/garm/**',route=>{posts++;return route.abort();});
 await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();
 await page.getByRole('button',{name:'Load Garm settings'}).click();
 await page.getByLabel('Pool priority').fill('19');await page.getByLabel('Garm audit reason').fill('keep unsent review draft');
 await page.getByRole('button',{name:'Review Garm change'}).click();
 await expect(page.getByRole('heading',{name:'Review actual Garm change'})).toBeVisible();
 await expect(page.getByLabel('Pool priority')).toBeHidden();
 await page.getByLabel('Garm management write token').fill('disposable-not-sent');
 await page.getByRole('button',{name:'Cancel Garm review'}).click();
 await expect(page.getByLabel('Pool priority')).toHaveValue('19');
 await expect(page.getByLabel('Garm audit reason')).toHaveValue('keep unsent review draft');
 await page.getByRole('button',{name:'Review Garm change'}).click();
 await expect(page.getByText('priority: 19',{exact:true})).toBeVisible();
 await expect(page.getByLabel('Garm management write token')).toHaveValue('');
 await page.getByRole('button',{name:'Cancel Garm review'}).click();
 await page.getByRole('button',{name:'Discard draft and refresh Garm settings'}).click();
 await expect(page.getByLabel('Pool priority')).toHaveValue('0');
 await expect(page.getByLabel('Garm audit reason')).toHaveValue('');
 expect(posts).toBe(0);
});
