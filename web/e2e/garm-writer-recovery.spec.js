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
