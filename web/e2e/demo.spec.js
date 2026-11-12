import { test, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';
const fixture = JSON.parse(readFileSync(new URL('../src/fixture.json', import.meta.url)));
const disposableTestValue = 'x'.repeat(32);

test('fixture dashboard, anchor navigation, exit and browser history', async ({ page }) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto('/?preview=1');
  await expect(page.getByRole('heading', { name: 'Capacity overview' })).toBeVisible();
  await expect(page.getByText('Simulation only. No capacity is enforced.')).toBeVisible();
  await expect(page.getByRole('row')).toHaveCount(3);
  await page.getByRole('link', { name: 'Repository pools' }).click();
  await expect(page).toHaveURL(/#pools$/);
  await page.getByRole('link', { name: 'Overview', exact: true }).click();
  await page.screenshot({ path: 'test-results/dashboard-desktop.png', fullPage: true });
  await page.getByRole('button', { name: 'Exit preview' }).click();
  await expect(page.getByRole('heading', { name: 'Meet the control plane.' })).toBeVisible();
  await page.goBack();
  await expect(page.getByRole('heading', { name: 'Capacity overview' })).toBeVisible();
  await page.goForward();
  await expect(page.getByRole('heading', { name: 'Meet the control plane.' })).toBeVisible();
  expect(errors).toEqual([]);
});

test('mobile preview has no page-wide horizontal overflow', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/?preview=1');
  await expect(page.getByRole('heading', { name: 'Capacity overview' })).toBeVisible();
  const dimensions = await page.evaluate(() => ({ document: document.documentElement.scrollWidth, viewport: window.innerWidth }));
  expect(dimensions.document).toBeLessThanOrEqual(dimensions.viewport);
  await page.screenshot({ path: 'test-results/dashboard-mobile.png', fullPage: true });
});

test('repeat connect and disconnect does not persist a demo token', async ({ page }) => {
  await page.route('**/api/v1/overview', async route => {
    expect(route.request().headers().authorization).toBe(`Bearer ${disposableTestValue}`);
    await route.fulfill({ json: fixture });
  });
  await page.goto('/');
  for (let i = 0; i < 2; i++) {
    await page.getByLabel('Disposable demo token').fill(disposableTestValue);
    await page.getByRole('button', { name: 'Open read-only demo' }).click();
    await expect(page.getByRole('heading', { name: 'Capacity overview' })).toBeVisible();
    await page.getByRole('button', { name: 'Disconnect' }).click();
    await expect(page.getByLabel('Disposable demo token')).toHaveValue('');
  }
  expect(await page.evaluate(() => ({ local: localStorage.length, session: sessionStorage.length, cookies: document.cookie }))).toEqual({ local: 0, session: 0, cookies: '' });
});

test('unauthorized response never shows fixture data as live', async ({ page }) => {
  await page.route('**/api/v1/overview', route => route.fulfill({ status: 401, body: 'unauthorized' }));
  await page.goto('/');
  await page.getByLabel('Disposable demo token').fill(disposableTestValue);
  await page.getByRole('button', { name: 'Open read-only demo' }).click();
  await expect(page.getByRole('alert')).toHaveText('That demo token was not accepted.');
  await expect(page.getByRole('heading', { name: 'Capacity overview' })).toHaveCount(0);
});

test('cancel ignores a late response and clears the token', async ({ page }) => {
  let release;
  let finished;
  let started;
  const entered = new Promise(resolve => { started = resolve; });
  const blocked = new Promise(resolve => { release = resolve; });
  const complete = new Promise(resolve => { finished = resolve; });
  await page.route('**/api/v1/overview', async route => {
    started();
    await blocked;
    try { await route.fulfill({ json: fixture }); } catch { /* Aborted browser request. */ } finally { finished(); }
  });
  await page.goto('/');
  await page.getByLabel('Disposable demo token').fill(disposableTestValue);
  await page.getByRole('button', { name: 'Open read-only demo' }).click();
  await entered;
  await expect(page.getByRole('button', { name: 'Connecting…' })).toBeDisabled();
  await page.getByRole('button', { name: 'Cancel' }).click();
  release();
  await complete;
  await expect(page.getByLabel('Disposable demo token')).toHaveValue('');
  await expect(page.getByRole('heading', { name: 'Capacity overview' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Open read-only demo' })).toBeEnabled();
});
