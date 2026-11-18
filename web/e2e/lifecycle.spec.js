import {expect} from '@playwright/test';
import {test} from './fixtures.js';
test('lifecycle status is read only, follows refresh and disappears on disconnect',async({page})=>{
 const writes=[];page.on('request',r=>{if(r.url().includes('/admin/api/')&&r.method()!=='GET')writes.push(r.method());});
 await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));
 const response=page.waitForResponse(r=>new URL(r.url()).pathname==='/admin/api/v1/overview');
 await page.getByRole('button',{name:'Connect read-only'}).click();const original=await(await response).json();
 const panel=page.locator('#lifecycle');await expect(panel).toContainText('Provisioning is off');
 await panel.getByText('Retirement assessment for host-a',{exact:true}).click();
 await expect(panel).toContainText('job-assignment fence');
 await page.route('**/admin/api/v1/overview',r=>r.fulfill({status:200,contentType:'application/json',json:{...original,states:{unknown:3,creating:2},hosts:[]}}));
 await page.getByRole('button',{name:'Refresh snapshot',exact:true}).click();
 await expect(panel).toContainText('Outcome unknown');await expect(panel).toContainText('Pending creation');await expect(panel).not.toContainText('host-a');
 await expect(panel.getByRole('button')).toHaveCount(0);
 await page.getByRole('button',{name:'Disconnect',exact:true}).click();await expect(panel).toHaveCount(0);expect(writes).toEqual([]);
});

test('mobile lifecycle keeps table scroll local and expanded blockers within viewport',async({page})=>{
 await page.setViewportSize({width:320,height:800});await page.goto('/');
 await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();
 const panel=page.locator('#lifecycle');await expect(panel).toBeVisible();
 await panel.getByText('Retirement assessment for host-a',{exact:true}).click();
 await expect(panel).toContainText('job_assignment_fence_unqualified');
 const table=panel.getByRole('region',{name:'Recorded lifecycle states',exact:true});
 await expect(table).toBeVisible();
 expect(await table.evaluate(el=>el.scrollWidth>el.clientWidth)).toBe(true);
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});
