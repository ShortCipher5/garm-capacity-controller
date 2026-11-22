import { expect } from '@playwright/test';
import { test } from './fixtures.js';

test('unknown stock write offers redacted diagnostic download without replay or force controls', async ({page}) => {
 let mutations=0;
 await page.route('**/admin/api/v1/garm**',route=>{
  if(route.request().method()!=='GET'){mutations++;return route.abort();}
  return route.fulfill({json:{settings_available:true,writes_enabled:true,write_blocked:true,history:[],history_truncated:false,pending:{request_id:'11111111-1111-4111-8111-111111111111',kind:'pool',target:'pool',started_at:'2026-11-18T00:00:00Z',resolution_available:false},settings:{pools:[],repositories:[],writes_enabled:true}}});
 });
 await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();await page.getByRole('button',{name:'Load Garm settings'}).click();
 const panel=page.getByRole('complementary',{name:'Garm writer recovery'});
 await expect(panel).toBeVisible();await expect(panel.getByText(/Do not resend it with a new request ID/)).toBeVisible();
 const download=page.waitForEvent('download');await panel.getByRole('button',{name:'Download writer diagnostic summary'}).click();expect((await download).suggestedFilename()).toBe('garm-writer-recovery.json');
 await expect(page.getByRole('button',{name:'Review Garm change'})).toBeDisabled();
 await expect(page.getByRole('button',{name:/force|reset|clear journal/i})).toHaveCount(0);expect(mutations).toBe(0);
});

test('failed read presents recovery guidance and never claims refresh restores authority',async({page})=>{
 await page.route('**/admin/api/v1/garm',route=>route.fulfill({status:503,json:{error:'unavailable'}}));
 await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();await page.getByRole('button',{name:'Load Garm settings'}).click();
 const panel=page.getByRole('complementary',{name:'Garm writer recovery'});await expect(panel).toBeVisible();await expect(panel.getByText(/a page refresh does not restore that authority/)).toBeVisible();await expect(panel.getByText(/No reset, force-write/)).toBeVisible();
});

test('failed explicit refresh preserves only a dismissible historical request', async ({page}) => {
 const id='11111111-1111-4111-8111-111111111111';
 let mode='pending', mutations=0;
 await page.route('**/admin/api/v1/garm',route=>{
  if(route.request().method()!=='GET'){mutations++;return route.abort();}
  if(mode==='unavailable')return route.fulfill({status:503,json:{error:'unavailable'}});
  const pending=mode==='pending'?{request_id:id,kind:'pool',target:'private-target',started_at:'2026-11-18T00:00:00Z',state:'unknown',resolution_available:false}:undefined;
  return route.fulfill({json:{settings_available:true,writes_enabled:true,write_blocked:!!pending,history:[],history_truncated:false,pending,settings:{pools:[],repositories:[],writes_enabled:true}}});
 });
 await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();await page.getByRole('button',{name:'Load Garm settings'}).click();
 await expect(page.getByText(/Unresolved request/)).toContainText(id);
 mode='unavailable';await page.getByRole('button',{name:'Refresh Garm settings',exact:true}).click();
 const historical=page.getByRole('complementary',{name:'Historical unresolved Garm request'});
 await expect(historical).toContainText(id);await expect(historical).toContainText('last observed: unknown');
 await expect(historical).not.toContainText('private-target');
 await expect(page.getByRole('button',{name:'Resolve pending write'})).toHaveCount(0);
 await expect(page.getByRole('button',{name:'Review Garm change'})).toHaveCount(0);
 await historical.getByRole('button',{name:'Dismiss historical request'}).click();
 await expect(historical).toHaveCount(0);
 await page.getByRole('button',{name:'Load Garm settings'}).click();
 await expect(page.getByRole('button',{name:'Load Garm settings'})).toBeEnabled();await expect(historical).toHaveCount(0);
 mode='pending';await page.getByRole('button',{name:'Load Garm settings'}).click();await expect(page.getByText(/Unresolved request/)).toContainText(id);
 mode='unavailable';await page.getByRole('button',{name:'Refresh Garm settings',exact:true}).click();await expect(historical).toBeVisible();
 mode='clear';await page.getByRole('button',{name:'Load Garm settings'}).click();
 await expect(page.getByRole('button',{name:'Refresh Garm settings',exact:true})).toBeEnabled();await expect(historical).toHaveCount(0);
 expect(mutations).toBe(0);
});
