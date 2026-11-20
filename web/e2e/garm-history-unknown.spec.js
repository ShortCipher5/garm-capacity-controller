import {expect} from '@playwright/test';import {test} from './fixtures.js';
test('confirmed historical receipts never unlock a different unknown managed write',async({page})=>{
 const unknown='11111111-1111-4111-8111-111111111111',confirmed='22222222-2222-4222-8222-222222222222',id='00000000-0000-4000-8000-000000000001';let posts=0;
 const pool={id,provider_name:'capacity',fingerprint:'f'.repeat(64),enabled:true,priority:0,max_runners:4,min_idle_runners:1,generation:1,tags:['linux']};
 const value={maximum_reduction_only:true,managed_history_available:true,settings_available:true,writes_enabled:true,write_blocked:true,history_truncated:true,pending:{request_id:unknown,kind:'pool',target:id,started_at:'2026-11-18T10:00:00Z',resolution_available:false},history:[{request_id:confirmed,kind:'pool',target:id,state:'confirmed',reason:'reviewed decrease',created_at:'2026-11-18T09:00:00Z',before:{max_runners:5},desired:{max_runners:4},result:{max_runners:4}}],settings:{pools:[pool],repositories:[],writes_enabled:true}};
 await page.route('**/admin/api/v1/garm',route=>route.fulfill({status:200,json:value}));
 await page.route('**/admin/api/v1/garm/**',route=>{posts++;return route.abort();});
 await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();await page.getByRole('button',{name:'Load Garm settings'}).click();
 await expect(page.getByRole('heading',{name:'Historical Garm writer receipts'})).toBeVisible();await expect(page.getByText('Confirmation of another request never clears',{exact:false})).toBeVisible();await expect(page.getByText('Unresolved request '+unknown+': pool / '+id,{exact:true})).toBeVisible();
 await expect(page.getByRole('button',{name:'Review Garm change'})).toBeDisabled();await expect(page.getByRole('button',{name:'Resolve pending write'})).toHaveCount(0);
 await page.getByRole('button',{name:'Refresh Garm settings',exact:true}).click();await expect(page.getByRole('button',{name:'Review Garm change'})).toBeDisabled();expect(posts).toBe(0);
 value.managed_history_available=false;await page.getByRole('button',{name:'Refresh Garm settings',exact:true}).click();await expect(page.getByText('Authenticated supervisor history is unavailable',{exact:false})).toBeVisible();await expect(page.getByRole('heading',{name:'Historical Garm writer receipts'})).toHaveCount(0);await expect(page.getByRole('button',{name:'Review Garm change'})).toBeDisabled();expect(posts).toBe(0);
});
