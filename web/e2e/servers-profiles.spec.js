import { expect } from "@playwright/test";
import { test } from "./fixtures.js";

const read = "a".repeat(64);
const write = "c".repeat(64);

async function connect(page) {
  await page.goto("/");
  await page.getByLabel("Admin read token").fill(read);
  await page.getByRole("button", { name: "Connect read-only" }).click();
  await expect(page.getByRole("heading", { name: "Allocation overview" })).toBeVisible();
}

test("read-only discovery, reviewed enrollment and honest duplicate error", async ({ page }) => {
  await connect(page);
  await page.getByRole("link", { name: "Servers", exact: true }).click();
  const discovery = page.locator("#server-discovery");
  await discovery.getByRole("button", { name: "Load discovery profiles", exact: true }).click();
  await expect(discovery.getByText("candidate-discover", { exact: true })).toBeVisible();
  await discovery.getByLabel("Discovery management token", { exact: true }).fill(write);
  await discovery.getByRole("button", { name: "Discover candidate-discover", exact: true }).click();
  await expect(discovery.getByText(/Physical totals \(complete\): CPU 8,000 millicores · Memory 32,768 MiB · Disk 524,288 MiB \(131,072 MiB used\)/)).toBeVisible();
  const suggestion = discovery.getByRole("table", { name: "Workload-aware suggested budget", exact: true });
  for (const [resource, reserve, budget] of [
    ["CPU", "1,000 millicores", "4,500 millicores"],
    ["Memory", "3,277 MiB", "24,371 MiB"],
    ["Disk", "52,429 MiB", "315,187 MiB"],
  ]) {
    const row = suggestion.getByRole("row").filter({ has: page.getByRole("rowheader", { name: resource, exact: true }) });
    await expect(row.getByRole("cell")).toHaveText([reserve, budget]);
  }
  await expect(discovery.locator("[data-unmanaged]")).toHaveText(/1 unmanaged guest\(s\)/);
  await expect(discovery.getByText(/unmanaged guests: the shared limit may not cover them/)).toBeVisible();
  await expect(discovery.getByText(/physical_capacity_scope_unqualified/)).toBeVisible();
  await expect(discovery.locator("tr", { hasText: "fixture-runner" })).toContainText("Managed runner");
  await expect(discovery.locator("tr", { hasText: "fixture-sidecar" })).toContainText("Unmanaged");
  await discovery.getByLabel("Discovery management token", { exact: true }).fill(write);
  await discovery.getByRole("button", { name: "Discover candidate-discover", exact: true }).click();
  await discovery.getByRole("button", { name: "Enroll this host", exact: true }).click();
  await expect(discovery.getByText("Plan shared runner capacity", { exact: true })).toBeVisible();
  await expect(discovery.getByLabel("Budget memory MiB", { exact: true })).toHaveValue("24371");
  await discovery.getByLabel("Budget memory MiB", { exact: true }).fill("32000");
  await expect(discovery.getByText(/only 768 remains outside/)).toBeVisible();
  await discovery.getByLabel("Budget memory MiB", { exact: true }).fill("24371");
  await discovery.getByLabel("large planned count", { exact: true }).fill("2");
  await expect(discovery.getByText(/Exceeds shared budget/)).toBeVisible();
  await discovery.getByLabel("large planned count", { exact: true }).fill("0");
  await discovery.getByLabel("New host name", { exact: true }).fill("host-b");
  await discovery.getByLabel("Budget runners", { exact: true }).fill("2");
  await discovery.getByLabel("Enrollment management token", { exact: true }).fill(write);
  await discovery.getByRole("button", { name: "Enroll host", exact: true }).click();
  await expect(discovery.getByText(/Host host-b enrolled using the reviewed discovery snapshot/)).toBeVisible();
  const enrolled = page.locator('#servers [data-host="host-b"]');
  await expect(enrolled.getByRole("heading", { name: "host-b", exact: true })).toBeVisible();
  await expect(enrolled.getByText(/CPU 8,000 millicores · Memory 32,768 MiB · Disk 524,288 MiB · Runners not measured by discovery/)).toBeVisible();
  await expect(enrolled.getByText("New claims paused")).toBeVisible();
  await discovery.getByRole("button", { name: "Enroll this host", exact: true }).click();
  await discovery.getByLabel("New host name", { exact: true }).fill("host-b");
  await discovery.getByLabel("Enrollment management token", { exact: true }).fill(write);
  await discovery.getByRole("button", { name: "Enroll host", exact: true }).click();
  await expect(discovery.getByText(/host already enrolled/)).toBeVisible();
});

test("usage chart renders measured history with a real gap and never fabricates missing history", async ({ page }) => {
  await connect(page);
  await page.getByRole("link", { name: "Servers", exact: true }).click();
  const measured = page.locator('#servers [data-host="host-a"]');
  await expect(measured.getByText("CPU used — measured, not allocated")).toBeVisible();
  await expect(measured.locator("polyline.history-line")).toHaveCount(2);
  await expect(measured.getByText(/Last sample .* ago · source fixture-collector/)).toBeVisible();
  await expect(measured.getByText(/CPU fair-use cap: 3,000 millicores/)).toBeVisible();
  await measured.getByLabel(/^Usage metric/).selectOption("ram_used_mib");
  await expect(measured.getByText("Memory used — measured, not allocated")).toBeVisible();
  const quiet = page.locator('#servers [data-host="host-b"]');
  await expect(quiet.getByText(/Usage history unavailable: no measured samples exist for this host/)).toBeVisible();
  await expect(quiet.locator("polyline.history-line")).toHaveCount(0);
});

test("runner-size profile CRUD, validation and per-host fit estimates including an honest zero", async ({ page }) => {
  await connect(page);
  // admin.spec.js leaves host-a disabled: estimates only cover enabled hosts,
  // so re-enable it through the existing reviewed management control first.
  await page.getByLabel(/^Policy control/).selectOption("host_mode");
  await page.getByLabel(/^Policy target/).selectOption("host-a");
  await page.getByLabel(/^New host mode/).selectOption("enabled");
  await page.getByLabel("Audit reason", { exact: true }).fill("re-enable host-a for estimate view");
  await page.getByRole("button", { name: "Review change", exact: true }).click();
  await page.getByLabel("Management write token", { exact: true }).fill(write);
  await page.getByRole("button", { name: "Apply reviewed change", exact: true }).click();
  await expect(page.getByText("Policy saved.")).toBeVisible();
  await page.getByRole("link", { name: "Profiles", exact: true }).click();
  const profiles = page.locator("#profile-management");
  await profiles.getByRole("button", { name: "New profile", exact: true }).click();
  await profiles.getByLabel("Profile name", { exact: true }).fill("E2E-Bad");
  await profiles.getByLabel("CPU (vCPUs)", { exact: true }).fill("2");
  await profiles.getByLabel("Memory (MiB)", { exact: true }).fill("4096");
  await profiles.getByLabel("Disk (MiB)", { exact: true }).fill("20480");
  await profiles.getByLabel("Profile management write token", { exact: true }).fill(write);
  await profiles.getByRole("button", { name: "Save profile", exact: true }).click();
  await expect(profiles.getByText(/Profile names use 1–64 lowercase letters, digits or hyphens/)).toBeVisible();
  await profiles.getByLabel("Profile name", { exact: true }).fill("e2e-small");
  // The write token is single-use and cleared after every attempt.
  await profiles.getByLabel("Profile management write token", { exact: true }).fill(write);
  await profiles.getByRole("button", { name: "Save profile", exact: true }).click();
  await expect(profiles.getByText("Runner-size profile e2e-small saved.")).toBeVisible();
  await expect(profiles.locator("tr", { hasText: "e2e-small" })).toContainText("~1 runners fit on host-a");
  await profiles.getByRole("button", { name: "New profile", exact: true }).click();
  await profiles.getByLabel("Profile name", { exact: true }).fill("e2e-large");
  await profiles.getByLabel("CPU (vCPUs)", { exact: true }).fill("4");
  await profiles.getByLabel("Memory (MiB)", { exact: true }).fill("8192");
  await profiles.getByLabel("Disk (MiB)", { exact: true }).fill("0");
  await profiles.getByLabel("Profile management write token", { exact: true }).fill(write);
  await profiles.getByRole("button", { name: "Save profile", exact: true }).click();
  await expect(profiles.locator("tr", { hasText: "e2e-large" })).toContainText("~0 runners fit on host-a (no remaining budget)");
  await profiles.locator("tr", { hasText: "e2e-small" }).getByRole("button", { name: "Edit", exact: true }).click();
  await profiles.getByLabel("Memory (MiB)", { exact: true }).fill("2048");
  await profiles.getByLabel("Profile management write token", { exact: true }).fill(write);
  await profiles.getByRole("button", { name: "Save profile", exact: true }).click();
  await expect(profiles.getByText("Runner-size profile e2e-small saved.")).toBeVisible();
  await expect(profiles.locator("tr", { hasText: "e2e-small" })).toContainText("2,048 MiB");
  await profiles.locator("tr", { hasText: "e2e-large" }).getByRole("button", { name: "Delete", exact: true }).click();
  await profiles.getByLabel("Deletion management token", { exact: true }).fill(write);
  await profiles.getByRole("button", { name: "Confirm delete e2e-large", exact: true }).click();
  await expect(profiles.getByText("Runner-size profile e2e-large deleted.")).toBeVisible();
  await expect(profiles.locator("tr", { hasText: "e2e-large" })).toHaveCount(0);
});

test('profile controls follow admin capability and discard a draft on downgrade', async ({page}) => {
 let enabled=false, writes=0;
 // Read the fixture through Chromium's exact pinned TLS session, not Node route.fetch.
 const initial=page.waitForResponse(response=>response.url().endsWith('/admin/api/v1/overview')&&response.request().method()==='GET'&&response.status()===200);
 await connect(page);
 const body=await (await initial).json();
 await page.route('**/admin/api/v1/overview',route=>route.fulfill({status:200,json:{...body,management_enabled:enabled}}));
 await page.route('**/admin/api/v1/profiles**',route=>{
  if(route.request().method()!=='GET'){writes++;return route.abort();}
  return route.continue();
 });
 await page.getByRole('link',{name:'Profiles',exact:true}).click();
 await page.getByRole('button',{name:'Refresh snapshot',exact:true}).click();
 const panel=page.locator('#profile-management');
 await expect(panel.getByRole('button',{name:'New profile',exact:true})).toHaveCount(0);
 await expect(panel.getByText('Profile management is disabled',{exact:false})).toBeVisible();
 enabled=true;await page.getByRole('button',{name:'Refresh snapshot',exact:true}).click();
 await panel.getByRole('button',{name:'New profile',exact:true}).click();
 await panel.getByLabel('Profile management write token').fill(write);
 enabled=false;await page.getByRole('button',{name:'Refresh snapshot',exact:true}).click();
 await expect(panel.getByLabel('Profile management write token')).toHaveCount(0);
 await expect(panel.getByRole('button',{name:'Save profile',exact:true})).toHaveCount(0);
 enabled=true;await page.getByRole('button',{name:'Refresh snapshot',exact:true}).click();
 await panel.getByRole('button',{name:'New profile',exact:true}).click();
 await expect(panel.getByLabel('Profile management write token')).toHaveValue('');
 await expect(page).toHaveURL(/#profiles$/);
 expect(writes).toBe(0);
});
