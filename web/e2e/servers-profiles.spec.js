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
  await expect(discovery.getByText(/Backend-reported totals \(complete\): CPU 8,000 millicores · Memory 32,768 MiB · Disk 524,288 MiB \(131,072 MiB used\)/)).toBeVisible();
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
  const useMix = discovery.getByRole("button", { name: "Use planned total as runner limit", exact: true });
  const runnerLimit = discovery.getByLabel("Budget runners", { exact: true });
  await expect(runnerLimit).toHaveValue("0");
  await expect(useMix).toBeDisabled();
  await discovery.getByLabel("large planned count", { exact: true }).fill("2");
  await expect(discovery.getByText(/Exceeds shared budget/)).toBeVisible();
  await expect(useMix).toBeDisabled();
  await discovery.getByLabel("large planned count", { exact: true }).fill("0");
  await discovery.getByLabel("small planned count", { exact: true }).fill("2");
  await expect(useMix).toBeEnabled();
  const premature = [];
  page.on("request", (request) => { if (request.method() === "POST" && new URL(request.url()).pathname === "/admin/api/v1/hosts") premature.push(request); });
  await useMix.click();
  await expect(runnerLimit).toHaveValue("2");
  expect(premature).toHaveLength(0);
  await runnerLimit.fill("3");
  await expect(runnerLimit).toHaveValue("3");
  await discovery.getByLabel("New host name", { exact: true }).fill("host-b");
  await runnerLimit.fill("2");
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

  await enrolled.getByRole("link", { name: "Manage host-b in Ledger policy management" }).click();
  const controls = page.locator("#management");
  await expect(controls.getByLabel("Policy target")).toHaveValue("host-b");
  await expect(controls.getByRole("option", { name: "Enable placement after reconciliation" })).toBeDisabled();
  await expect(controls.getByText("No ledger pool is configured to place runners here.")).toBeVisible();
  await controls.getByLabel("New host mode").selectOption("draining");
  await controls.getByLabel("Audit reason").fill("exercise local drain request");
  await controls.getByRole("button", { name: "Review change" }).click();
  await expect(controls.getByText(/awaiting retirement evidence/)).toBeVisible();
  await controls.getByLabel("Management write token").fill(write);
  await controls.getByRole("button", { name: "Apply reviewed change" }).click();
  await expect(enrolled.getByText(/Drain requested. Existing runners may still receive jobs./)).toBeVisible();
  await expect(enrolled.getByRole("link", { name: "Inspect retirement blockers" })).toBeVisible();
});

test("usage chart renders observations without inventing interval coverage", async ({ page }) => {
  const recent = page.waitForResponse(response => {
    const url = new URL(response.url());
    return url.pathname === "/admin/api/v1/usage/host-a" &&
      url.search === "?limit=500&order=recent" && response.request().method() === "GET";
  });
  await connect(page);
  expect((await recent).status()).toBe(200);
  await page.getByRole("link", { name: "Servers", exact: true }).click();
  const measured = page.locator('#servers [data-host="host-a"]');
  await expect(measured.getByText("CPU used — recorded, not allocated")).toBeVisible();
  await expect(measured.locator("polyline.history-line")).toHaveCount(0);
  await expect(measured.locator(".usage-panel svg circle")).toHaveCount(5);
  await expect(measured.getByText(/Sampling cadence and interval coverage are not supplied/)).toBeVisible();
  await expect(measured.getByText(/Last sample .* ago · source fixture-collector/)).toBeVisible();
  await expect(measured.getByText(/CPU fair-use cap: 3,000 millicores/)).toBeVisible();
  await measured.getByLabel(/^Usage metric/).selectOption("ram_used_mib");
  await expect(measured.getByText("Memory used — recorded, not allocated")).toBeVisible();
  const quiet = page.locator('#servers [data-host="host-b"]');
  await expect(quiet.getByText(/Usage history unavailable: no measured samples exist for this host/)).toBeVisible();
  await expect(quiet.locator("polyline.history-line")).toHaveCount(0);
});

test("usage history deadline ends loading for only the held server card", async ({ page }) => {
  const installedAt = new Date();
  await page.clock.install({ time: installedAt });
  await page.clock.pauseAt(new Date(installedAt.getTime() + 1000));
  const held = Promise.withResolvers();
  const release = Promise.withResolvers();
  const settledRoute = Promise.withResolvers();
  let requests = 0;
  await page.route("**/admin/api/v1/usage/host-a?*", async route => {
    requests++;
    held.resolve();
    await release.promise;
    await route.abort().catch(() => {}); // The browser may already have cancelled the request.
    settledRoute.resolve();
  });
  try {
    await connect(page);
    await held.promise;
    await page.getByRole("link", { name: "Servers", exact: true }).click();
    const measured = page.locator('#servers [data-host="host-a"] .usage-panel');
    const quiet = page.locator('#servers [data-host="host-b"] .usage-panel');
    await expect(quiet.getByText(/Usage history unavailable: no measured samples exist for this host/)).toBeVisible();
    await page.clock.runFor(14_999);
    await expect(measured.getByText("Loading resource observations…")).toBeVisible();
    await page.clock.runFor(1);
    await expect(measured.getByRole("status")).toHaveText("Usage history read timed out. Refresh to retry.");
    await expect(measured.getByText("Loading resource observations…")).toHaveCount(0);
    await expect(measured.locator("svg")).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "Allocation overview" })).toBeVisible();
    await expect(quiet.getByText(/Usage history unavailable: no measured samples exist for this host/)).toBeVisible();
    expect(requests).toBe(1);
  } finally {
    release.resolve();
    if (requests > 0) await settledRoute.promise;
  }
});

test("runner-size profile CRUD uses the actual eligible-host estimates", async ({ page }) => {
  const overview = page.waitForResponse(r => r.url().endsWith("/admin/api/v1/overview") && r.ok());
  await connect(page);
  const snapshot = await (await overview).json();
  const enabled = snapshot.hosts.find(host => host.id === "host-a").mode === "enabled";
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
  await expect(profiles.locator("tr", { hasText: "e2e-small" })).toContainText(enabled ? "~1 runners fit on host-a" : "No enabled hosts");
  await profiles.getByRole("button", { name: "New profile", exact: true }).click();
  await profiles.getByLabel("Profile name", { exact: true }).fill("e2e-large");
  await profiles.getByLabel("CPU (vCPUs)", { exact: true }).fill("4");
  await profiles.getByLabel("Memory (MiB)", { exact: true }).fill("8192");
  await profiles.getByLabel("Disk (MiB)", { exact: true }).fill("0");
  await profiles.getByLabel("Profile management write token", { exact: true }).fill(write);
  await profiles.getByRole("button", { name: "Save profile", exact: true }).click();
  await expect(profiles.locator("tr", { hasText: "e2e-large" })).toContainText(enabled ? "~0 runners fit on host-a (no remaining budget)" : "No enabled hosts");
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

test("reviewed removal discards only a disabled discovery enrollment", async ({ page }) => {
  await connect(page);
  const enrolled = page.locator('#servers [data-host="host-b"]');
  await enrolled.getByRole("link", { name: "Manage host-b in Ledger policy management" }).click();
  const controls = page.locator("#management");
  await expect(controls.getByLabel("Policy target")).toHaveValue("host-b");
  await controls.getByLabel("Policy control").selectOption("host_draft_remove");
  await expect(controls.getByRole("button", { name: "Review change" })).toBeDisabled();
  await controls.getByLabel("Policy control").selectOption("host_mode");
  await controls.getByLabel("New host mode").selectOption("disabled");
  await controls.getByLabel("Audit reason").fill("stop new placement before withdrawal");
  await controls.getByRole("button", { name: "Review change" }).click();
  await controls.getByLabel("Management write token").fill(write);
  await controls.getByRole("button", { name: "Apply reviewed change" }).click();
  await expect(enrolled).toContainText("Configured mode: disabled");
  await controls.getByLabel("Policy control").selectOption("host_draft_remove");
  await expect(controls.getByLabel("Policy target")).toHaveValue("host-b");
  await controls.getByLabel("Audit reason").fill("discard unused enrollment");
  await controls.getByRole("button", { name: "Review change" }).click();
  await controls.getByLabel("Management write token").fill(write);
  await controls.getByRole("button", { name: "Apply reviewed change" }).click();
  await expect(controls.getByRole("status")).toContainText("Its alias remains reserved");
  await expect(enrolled).toHaveCount(0);
  await expect(page.locator('#servers [data-host="host-a"]')).toBeVisible();
});
