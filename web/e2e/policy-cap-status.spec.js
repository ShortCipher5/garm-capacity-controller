import {expect} from '@playwright/test';import {test} from './fixtures.js';
test('blocked cap process stays blocked despite historical success and performs no writes',async({page})=>{let posts=0;const pool={id:'00000000-0000-4000-8000-000000000001',provider_name:'capacity',fingerprint:'f'.repeat(64),enabled:true,priority:0,max_runners:4,min_idle_runners:1,generation:1,tags:['linux']};await page.route('**/admin/api/v1/garm',route=>route.fulfill({json:{maximum_reduction_only:true,settings_available:true,writes_enabled:true,write_blocked:true,history:[],history_truncated:false,settings:{pools:[pool],repositories:[],writes_enabled:true},policy_cap_reconciler:{pool_results:[{pool_id:pool.id,outcome:'unconfirmed'}],state:'blocked',policy_revision:7,confirmed:1,skipped:2,last_attempt:'2026-11-18T10:00:00Z',last_success:'2026-11-18T09:00:00Z',last_request_id:'11111111-1111-4111-8111-111111111111'}}}));await page.route('**/admin/api/v1/garm/**',route=>{posts++;return route.abort();});await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();await page.getByRole('button',{name:'Load Garm settings'}).click();const panel=page.getByLabel('Saved-cap reconciliation');await expect(panel.getByRole('heading',{name:'Saved-cap reconciliation: blocked'})).toBeVisible();await expect(panel.getByText('An earlier success does not clear this block',{exact:false})).toBeVisible();await expect(page.getByRole('button',{name:'Review Garm change'})).toBeDisabled();await panel.getByText('Last reported per-pool outcomes').click();await expect(panel.getByText('Reduction request unconfirmed; preserve the writer journal',{exact:false})).toBeVisible();expect(posts).toBe(0);await expect(page.getByText('saving a lower positive pool cap may asynchronously lower',{exact:false})).toBeVisible();});

test('refresh reads do not write but the separately configured reconciler may lower maxima',async({page})=>{
  let reads=0, writes=0;
  const pool={id:'00000000-0000-4000-8000-000000000001',provider_name:'capacity',fingerprint:'f'.repeat(64),enabled:true,priority:0,max_runners:4,min_idle_runners:1,generation:1,tags:['linux']};
  await page.route('**/admin/api/v1/garm',route=>{
    expect(route.request().method()).toBe('GET');
    reads++;
    return route.fulfill({json:{
      maximum_reduction_only:true, settings_available:true, writes_enabled:true, write_blocked:false,
      history:[], history_truncated:true,
      settings:{pools:[pool],repositories:[],writes_enabled:true},
      policy_cap_reconciler:{state:'running',policy_revision:7,confirmed:0,skipped:0,last_attempt:'2026-11-18T10:00:00Z'},
    }});
  });
  await page.route('**/admin/api/v1/garm/**',route=>{writes++;return route.abort();});
  await page.goto('/');
  await page.getByLabel('Admin read token').fill('a'.repeat(64));
  await page.getByRole('button',{name:'Connect read-only'}).click();
  await page.getByRole('button',{name:'Load Garm settings'}).click();
  await expect(page.getByRole('heading',{name:'Saved-cap reconciliation: running'})).toBeVisible();
  await expect(page.getByText('These refreshes do not change settings.',{exact:false})).toBeVisible();
  await expect(page.getByText('The separately configured saved-cap reconciler can automatically lower a pool maximum.',{exact:false})).toBeVisible();
  await expect(page.getByText('No automatic writes or recreation.',{exact:false})).toHaveCount(0);
  const before=reads;
  await page.getByRole('button',{name:'Refresh observations, keep draft'}).click();
  await expect.poll(()=>reads).toBeGreaterThan(before);
  expect(writes).toBe(0);
});
