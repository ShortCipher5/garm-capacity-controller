import {expect} from "@playwright/test";
import {test} from "./fixtures.js";

const token = "a".repeat(64);
const overviewRoute = "**/admin/api/v1/overview";
const timingRoute = "**/admin/api/v1/ci-health";
const resources = {cpu_millis: 0, ram_mib: 0, runners: 0};
const overview = {
  mode: "experimental-ledger", management_enabled: false, policy_revision: "a".repeat(64),
  experimental_provisioning_enabled: false, admission_ready: false, epoch: 0,
  immutable_execution_plans: 0, legacy_unplanned_reservations: 0, legacy_source_preparations: 0,
  observed_at: "2026-01-01T00:00:00Z", global: {budget: resources, allocated: resources},
  hosts: [], pools: [], states: {}, events: [],
};

async function login(page) {
  await page.getByLabel("Admin read token").fill(token);
  await page.getByRole("button", {name: "Connect read-only", exact: true}).click();
  await expect(page.getByRole("heading", {name: "Allocation overview"})).toBeVisible();
}
function timing(at) {
  return {state: "observed", observed_at: at, timing: {attempts: [{
    source: "fixture", repository_id: "9007199254740993", workflow_id: "7",
    run_id: "9007199254740995", attempt: "2", observed_at: at, status: "queued", jobs: [],
  }]}};
}

test("only attached-source marker enables the on-demand view, then loss of read authorization disconnects", async ({page}) => {
  let available = false, calls = 0, status = 200;
  await page.route(overviewRoute, (route) =>
    route.fulfill({json: {...overview, ...(available ? {ci_health_available: true} : {})}}));
  await page.route(timingRoute, (route) => {
    calls++;
    expect(route.request().method()).toBe("GET");
    expect(route.request().headers().authorization).toBe("Bearer " + token);
    if (status === 401) return route.fulfill({status});
    return route.fulfill({json: timing(new Date().toISOString())});
  });

  await page.goto("/");
  const nav = page.getByRole("link", {name: "CI Health", exact: true});
  const panel = page.getByRole("region", {name: "CI Health", exact: true});
  await expect(nav).toHaveCount(0);
  await login(page);
  await expect(nav).toHaveCount(0);
  expect(calls).toBe(0);

  available = true;
  await page.getByRole("button", {name: "Refresh snapshot", exact: true}).click();
  await expect(nav).toBeVisible();
  expect(calls).toBe(0);
  await nav.click();
  await panel.getByRole("button", {name: "Load timing snapshot"}).click();
  await expect(panel).toContainText("9007199254740995");
  expect(calls).toBe(1);

  status = 401;
  await panel.getByRole("button", {name: "Refresh timing snapshot"}).click();
  await expect(page.getByRole("heading", {name: "Connect to your ledger"})).toBeVisible();
  await expect(page.getByRole("alert")).toContainText("Read authorization ended");
  await expect(nav).toHaveCount(0);
  await expect(page.getByText("9007199254740995", {exact: true})).toHaveCount(0);
  expect(calls).toBe(2);
});

test("withdrawing marker fences an in-flight response; accepted view expires despite wall rollback", async ({page}) => {
  const now = new Date();
  await page.clock.install({time: now});
  let available = true, calls = 0, release, seen;
  const firstRead = new Promise((resolve) => { seen = resolve; });
  const gate = new Promise((resolve) => { release = resolve; });
  await page.route(overviewRoute, (route) =>
    route.fulfill({json: {...overview, ...(available ? {ci_health_available: true} : {})}}));
  await page.route(timingRoute, async (route) => {
    calls++;
    if (calls === 1) {
      seen();
      await gate;
    }
    await route.fulfill({json: timing(new Date(now.getTime() - 30_000).toISOString())}).catch(() => {});
  });
  await page.goto("/");
  await login(page);
  const panel = page.getByRole("region", {name: "CI Health", exact: true});
  await panel.getByRole("button", {name: "Load timing snapshot"}).click();
  await firstRead;
  available = false;
  await page.getByRole("button", {name: "Refresh snapshot", exact: true}).click();
  await expect(panel).toHaveCount(0);
  release();
  await expect(page.getByText("9007199254740995", {exact: true})).toHaveCount(0);

  available = true;
  await page.getByRole("button", {name: "Refresh snapshot", exact: true}).click();
  await panel.getByRole("button", {name: "Load timing snapshot"}).click();
  await expect(panel).toContainText("9007199254740995");
  // Advance monotonic time, then move the wall clock back without moving the
  // monotonic deadline. The (later) wall-only deadline must not keep rows alive.
  await page.clock.fastForward(60_000);
  await page.clock.setSystemTime(new Date(now.getTime() + 15_000));
  await page.clock.fastForward(31_000);
  await expect(panel).toContainText("The snapshot expired");
  await expect(panel).not.toContainText("9007199254740995");
  expect(calls).toBe(2); // Expiry did not fetch or reschedule a read.

  await page.getByRole("button", {name: "Disconnect", exact: true}).click();
  await expect(panel).toHaveCount(0);
});
