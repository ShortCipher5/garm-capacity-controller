import {expect} from '@playwright/test';
import {test} from './fixtures.js';
test('retained overhead is visible as an included subtotal, not runner supply',async({page})=>{
 const response=page.waitForResponse(r=>r.url().endsWith('/admin/api/v1/overview')&&r.status()===200);
 await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();
 const base=await(await response).json();
 const domain={cpu_millis:100,ram_mib:500,disk_mib:1000,runners:0},artifact={cpu_millis:0,ram_mib:0,disk_mib:1024,runners:0};
 const body=structuredClone(base);body.non_pool_overhead={domain,artifact};body.global.allocated={cpu_millis:100,ram_mib:500,disk_mib:2024,runners:0};
 await page.route('**/admin/api/v1/overview',route=>route.fulfill({status:200,json:body}));
 await page.getByRole('button',{name:'Refresh snapshot'}).click();
 const panel=page.getByRole('region',{name:'Retained non-pool overhead'});
 await expect(panel).toContainText('artifact-root overhead: 1024 MiB disk. Already included above.');
 await panel.getByText('Accounting details',{exact:true}).click();
 await expect(panel).toContainText('not attributed to pools or running-runner counts');
 await expect(panel.getByRole('row',{name:'Shared artifact roots 0 0 1024 0'})).toBeVisible();
 delete body.non_pool_overhead;await page.getByRole('button',{name:'Refresh snapshot'}).click();
 await expect(panel).toContainText('do not assume it is zero');
});
