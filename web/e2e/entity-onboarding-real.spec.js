import {expect} from '@playwright/test';
import {test} from './fixtures.js';
test('unmocked authenticated admin discovery reaches existing upstream pool',async({page})=>{
 const pool='00000000-0000-4000-8000-000000000001',repo='00000000-0000-4000-8000-000000000002';const requests=[];
 page.on('request',r=>{if(r.url().includes('/admin/api/v1/garm'))requests.push({method:r.method(),url:r.url()})});
 await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();
 const panel=page.getByRole('region',{name:'Existing GARM onboarding',exact:true});await panel.getByRole('button',{name:'Refresh existing entities'}).click();await panel.getByLabel('Existing entity',{exact:true}).selectOption('repository/'+repo);await panel.getByRole('button',{name:'View existing pools'}).click();
 await expect(panel.getByRole('heading',{name:'Pool '+pool,exact:true})).toBeVisible();
 await panel.getByRole('button',{name:'Refresh viewed entity'}).click();await expect(panel.getByLabel('Entity observation changes')).toContainText('0 changed');
 expect(requests.length).toBeGreaterThanOrEqual(3);expect(requests.every(r=>r.method==='GET'&&r.url.endsWith('/admin/api/v1/garm'))).toBe(true);await expect(page.getByLabel('Garm management write token')).toHaveCount(0);
 await page.getByRole('button',{name:'Disconnect',exact:true}).click();await expect(panel).toHaveCount(0);
});
