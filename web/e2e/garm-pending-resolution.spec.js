import { expect } from '@playwright/test';
import { test } from './fixtures.js';

for (const available of [false, true]) {
 test(`pending resolution is ${available ? 'explicitly credentialed' : 'unavailable for stock writes'}`, async ({ page }) => {
  const errors=[];page.on('pageerror',error=>errors.push(error.message));
  let calls=0;
  const pending={request_id:'11111111-1111-4111-8111-111111111111',kind:'pool',target:'pool',started_at:'2026-01-01T00:00:00Z',resolution_available:available};
  await page.route('**/admin/api/v1/garm',route=>route.fulfill({json:{settings_available:true,writes_enabled:true,write_blocked:true,history:[],history_truncated:false,pending,settings:{pools:[],repositories:[],writes_enabled:true}}}));
  await page.route('**/admin/api/v1/garm/resolve',route=>{
   calls++;
   expect(route.request().headers().authorization).toBe('Bearer '+'c'.repeat(64));
   expect(route.request().postDataJSON()).toEqual({request_id:pending.request_id});
   return route.fulfill({status:503,json:{error:'unknown'}});
  });
  await page.goto('/');
  await page.getByLabel('Admin read token').fill('a'.repeat(64));
  await page.getByRole('button',{name:'Connect read-only'}).click();
  await page.getByRole('button',{name:'Load Garm settings'}).click();
  if(!available){
   await expect(page.getByText(/Automatic receipt resolution is unavailable/)).toBeVisible();
   await expect(page.getByRole('button',{name:'Resolve pending write'})).toHaveCount(0);
  }else{
   await page.getByLabel('Resolution management token').fill('c'.repeat(64));
   await page.getByRole('button',{name:'Resolve pending write'}).click();
   await expect(page.getByRole('status').filter({hasText:'outcome still unknown'})).toBeVisible();
   await expect(page.getByLabel('Resolution management token')).toHaveValue('');
   expect(calls).toBe(1);
   await expect(page.getByRole('button',{name:'Review Garm change'})).toBeDisabled();
  }
  expect(errors).toEqual([]);
  if(!available)expect(calls).toBe(0);
 });
}
