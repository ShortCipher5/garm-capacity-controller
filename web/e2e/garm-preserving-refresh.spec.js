import { expect } from '@playwright/test';
import { test } from './fixtures.js';

test('explicit observation refresh preserves draft and latches external conflict without a write',async({page})=>{
 let changed=false,gets=0,posts=0;
 const pool={id:'00000000-0000-4000-8000-000000000001',provider_name:'capacity',fingerprint:'f'.repeat(64),enabled:true,priority:0,max_runners:4,min_idle_runners:1,generation:1,tags:['linux']};
 await page.route('**/admin/api/v1/garm',route=>{
  gets++;
  const current=changed?{...pool,priority:7,fingerprint:'b'.repeat(64)}:pool;
  return route.fulfill({status:200,json:{settings_available:true,writes_enabled:true,write_blocked:false,history:[],history_truncated:false,settings:{pools:[current],repositories:[],writes_enabled:true}}});
 });
 await page.route('**/admin/api/v1/garm/**',route=>{posts++;return route.abort();});
 await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();
 await page.getByRole('button',{name:'Load Garm settings'}).click();
 await page.getByLabel('Pool priority').fill('19');await page.getByLabel('Garm audit reason').fill('keep draft during explicit refresh');
 const previous=gets;
 await page.getByRole('button',{name:'Refresh observations, keep draft'}).click();
 await expect.poll(()=>gets).toBeGreaterThan(previous);
 await expect(page.getByLabel('Pool priority')).toHaveValue('19');await expect(page.getByLabel('Garm audit reason')).toHaveValue('keep draft during explicit refresh');
 await expect(page.getByRole('button',{name:'Review Garm change'})).toBeEnabled();
 changed=true;
 await page.getByRole('button',{name:'Refresh observations, keep draft'}).click();
 await expect(page.getByText('Garm settings changed elsewhere.',{exact:false})).toBeVisible();
 await expect(page.getByLabel('Pool priority')).toHaveValue('19');await expect(page.getByLabel('Garm audit reason')).toHaveValue('keep draft during explicit refresh');
 await expect(page.getByRole('button',{name:'Review Garm change'})).toBeDisabled();
 await expect(page.getByRole('region',{name:'Observed Garm changes',exact:true})).toContainText('priority: 0 → 7');
 await page.getByRole('button',{name:'Dismiss change notice'}).click();
 await expect(page.getByRole('region',{name:'Observed Garm changes',exact:true})).toHaveCount(0);
 await expect(page.getByText('Garm settings changed elsewhere.',{exact:false})).toBeVisible();
 await expect(page.getByRole('button',{name:'Review Garm change'})).toBeDisabled();
 await expect(page.getByLabel('Pool priority')).toHaveValue('19');
 await page.getByRole('button',{name:'Discard draft and refresh Garm settings'}).click();
 await expect(page.getByLabel('Pool priority')).toHaveValue('7');await expect(page.getByLabel('Garm audit reason')).toHaveValue('');expect(posts).toBe(0);
});

test('clean existing settings reconcile read-side with a visible notice and no writes',async({page})=>{
 let priority=0,posts=0;
 const pool={id:'00000000-0000-4000-8000-000000000001',provider_name:'capacity',enabled:true,max_runners:4,min_idle_runners:1,generation:1,tags:['linux']};
 await page.route('**/admin/api/v1/garm',route=>route.fulfill({status:200,json:{settings_available:true,writes_enabled:true,write_blocked:false,history:[],history_truncated:false,settings:{pools:[{...pool,priority,fingerprint:(priority?'b':'f').repeat(64)}],repositories:[],writes_enabled:true}}}));
 await page.route('**/admin/api/v1/garm/**',route=>{posts++;return route.abort();});
 await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();
 await page.getByRole('button',{name:'Load Garm settings'}).click();
 priority=8;await page.getByRole('button',{name:'Refresh observations, keep draft'}).click();
 await expect(page.getByLabel('Pool priority')).toHaveValue('8');
 await expect(page.getByRole('region',{name:'Observed Garm changes',exact:true})).toContainText('priority: 0 → 8');
 await expect(page.getByText('Garm settings changed elsewhere.',{exact:false})).toHaveCount(0);
 await page.getByRole('button',{name:'Refresh observations, keep draft'}).click();
 await expect(page.getByRole('region',{name:'Observed Garm changes',exact:true})).toBeVisible();
 await page.getByRole('button',{name:'Dismiss change notice'}).click();
 await page.getByRole('button',{name:'Refresh observations, keep draft'}).click();
 await expect(page.getByRole('region',{name:'Observed Garm changes',exact:true})).toHaveCount(0);
 expect(posts).toBe(0);
});
