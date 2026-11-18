import {expect} from '@playwright/test';import {test} from './fixtures.js';
const token='c'.repeat(64),digest='d'.repeat(64);
const target={id:'candidate',address:'192.0.2.20',port:22,user:'inventory',credential_ref:'approved-key',host_key_fingerprint:'SHA256:'+'a'.repeat(43),review_digest:digest};
const result={target_id:'candidate',review_digest:digest,inventory:{source:'authenticated_linux_os_view',observed_at:'2026-11-18T04:00:00Z',complete:false,physical_capacity_qualified:false,logical_cpus:8,ram_mib:16384,available_ram_mib:0}};
async function open(page){await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();await page.getByRole('link',{name:'Servers',exact:true}).click();await page.locator('#ssh-inventory summary').click();await page.getByLabel('SSH inventory token',{exact:true}).fill(token);await page.getByRole('button',{name:'Load approved SSH targets'}).click();await expect(page.getByLabel('SSH target',{exact:true})).toBeVisible();}
test('SSH review binds displayed target, clears token and displays unknown dimensions',async({page})=>{
 const posts=[];await page.route('**/admin/api/v1/ssh-inventory',route=>{if(route.request().method()==='GET')return route.fulfill({json:{targets:[target]}});posts.push(route.request().postDataJSON());return route.fulfill({json:result});});
 await open(page);await expect(page.getByLabel('SSH inventory token',{exact:true})).toHaveValue('');await expect(page.getByRole('button',{name:'Run reviewed SSH inventory'})).toBeDisabled();
 await page.getByLabel('Verified SSH host key').check();await page.getByLabel('SSH inventory token',{exact:true}).fill(token);await page.getByRole('button',{name:'Run reviewed SSH inventory'}).click();
 await expect(page.getByRole('heading',{name:'Authenticated OS observation'})).toBeVisible();await expect(page.getByText(/Root filesystem: unknown MiB/)).toBeVisible();await expect(page.getByLabel('SSH inventory token',{exact:true})).toHaveValue('');
 expect(posts).toHaveLength(1);expect(posts[0].target_id).toBe('candidate');expect(posts[0].review_digest).toBe(digest);expect(posts[0].verified_host_key).toBe(true);expect(posts[0].address).toBeUndefined();
 await page.locator('#ssh-inventory').screenshot({path:'/tmp/garm-ssh-inventory-ui.png'});
 await page.locator('#ssh-inventory summary').click();await page.locator('#ssh-inventory summary').click();await expect(page.getByLabel('SSH target',{exact:true})).toHaveCount(0);await expect(page.getByRole('heading',{name:'Authenticated OS observation'})).toHaveCount(0);
});
test('cancelled pending SSH observation cannot restore review or late result',async({page})=>{
 let finish;const pending=new Promise(resolve=>finish=resolve);let started=false;
 await page.route('**/admin/api/v1/ssh-inventory',async route=>{if(route.request().method()==='GET')return route.fulfill({json:{targets:[target]}});started=true;await pending;await route.fulfill({json:result}).catch(()=>{});});
 await open(page);await page.getByLabel('Verified SSH host key').check();await page.getByLabel('SSH inventory token',{exact:true}).fill(token);await page.getByRole('button',{name:'Run reviewed SSH inventory'}).click();await expect.poll(()=>started).toBe(true);
 await expect(page.getByRole('button',{name:'Load approved SSH targets'})).toBeDisabled();await page.getByRole('button',{name:'Cancel SSH inventory'}).click();finish();
 await expect(page.getByLabel('SSH target',{exact:true})).toHaveCount(0);await expect(page.getByRole('heading',{name:'Authenticated OS observation'})).toHaveCount(0);await expect(page.getByLabel('SSH inventory token',{exact:true})).toHaveValue('');
});
