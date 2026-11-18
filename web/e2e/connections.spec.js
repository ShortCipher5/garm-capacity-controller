import {expect} from '@playwright/test';import {test} from './fixtures.js';
test('guided connection review, cancellation, discovery and local revocation',async({page})=>{
 await page.goto('/');await page.getByLabel('Admin read token',{exact:true}).fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only',exact:true}).click();await page.getByRole('link',{name:'Servers',exact:true}).click();
 const panel=page.locator('#connection-management');await panel.locator('summary').click();
 const token=panel.getByLabel('Connection management token',{exact:true});await token.fill('c'.repeat(64));await panel.getByRole('button',{name:'Reload connections',exact:true}).click();
 await panel.getByRole('button',{name:'Add connection draft',exact:true}).click();await panel.getByLabel('Connection profile ID',{exact:true}).fill('dynamic-ui');
 await token.fill('c'.repeat(64));await panel.getByRole('button',{name:'Save connection draft',exact:true}).click();await expect(panel.getByRole('button',{name:'Review key for dynamic-ui',exact:true})).toBeVisible();
 await panel.getByRole('button',{name:'Review key for dynamic-ui',exact:true}).click();await expect(panel.getByRole('button',{name:'Approve reviewed connection key',exact:true})).toBeDisabled();
 await panel.getByRole('button',{name:'Cancel review',exact:true}).click();await expect(panel.getByRole('button',{name:'Approve reviewed connection key',exact:true})).toHaveCount(0);
 await panel.getByRole('button',{name:'Review key for dynamic-ui',exact:true}).click();await panel.getByRole('checkbox').check();await token.fill('c'.repeat(64));await panel.getByRole('button',{name:'Approve reviewed connection key',exact:true}).click();
 await expect(panel.locator('li').filter({hasText:'dynamic-ui'})).toContainText('approved');
 const discovery=page.locator('#server-discovery');await discovery.getByRole('button',{name:'Load discovery profiles',exact:true}).click();await expect(discovery.getByRole('button',{name:'Discover dynamic-ui',exact:true})).toBeVisible();
 await discovery.getByLabel('Discovery management token',{exact:true}).fill('c'.repeat(64));await discovery.getByRole('button',{name:'Discover dynamic-ui',exact:true}).click();await expect(discovery.getByText('Discovery observation · dynamic-ui',{exact:true})).toBeVisible();
 await token.fill('c'.repeat(64));await panel.getByRole('button',{name:'Revoke local connection dynamic-ui',exact:true}).click();await expect(panel.locator('li').filter({hasText:'dynamic-ui'})).toContainText('revoked');
 await discovery.getByRole('button',{name:'Load discovery profiles',exact:true}).click();await expect(discovery.getByRole('button',{name:'Discover dynamic-ui',exact:true})).toHaveCount(0);
 await expect(token).toHaveValue('');
});
