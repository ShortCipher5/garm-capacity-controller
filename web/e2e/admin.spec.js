import { expect } from "@playwright/test";
import { test } from "./fixtures.js";
const token = "a".repeat(64);
async function connect(page) {
  await page.goto("/");
  await page.getByLabel("Admin read token").fill(token);
  await page.getByRole("button", { name: "Connect read-only" }).click();
  await expect(
    page.getByRole("heading", { name: "Allocation overview" }),
  ).toBeVisible();
}
test("real TLS handler + SQLite snapshot, read-only requests, refresh and reload", async ({
  page,
}) => {
  const api = [];
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("request", (request) => {
    if (request.url().includes("/admin/api/")) api.push(request);
  });
  await connect(page);
  await expect(
    page.getByRole("heading", { name: "host-a", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText(
      "Not drained. Existing idle runners may still receive jobs.",
    ),
  ).toBeVisible();
  await expect(
    page.getByText("Provisioning off · Admission barrier ready"),
  ).toBeVisible();
  await expect(page.locator(".metric").first()).toContainText("1,000");
  await expect(page.locator(".states")).toContainText("unknown");
  await page.getByRole("button", { name: "Refresh snapshot" }).dblclick();
  await expect(
    page.getByRole("button", { name: "Refresh snapshot" }),
  ).toBeEnabled();
  expect(api.length).toBeGreaterThan(1);
  for (const request of api) {
    expect(request.method()).toBe("GET");
    expect(new URL(request.url()).search).toBe("");
    expect(request.headers().authorization).toBe(`Bearer ${token}`);
  }
  expect(
    await page.evaluate(() => ({
      local: Object.keys(localStorage),
      session: Object.keys(sessionStorage),
    })),
  ).toEqual({ local: [], session: [] });
  await page.screenshot({
    path: "test-results/screenshots/desktop.png",
    fullPage: true,
  });
  await page.reload();
  await expect(page.getByLabel("Admin read token")).toHaveValue("");
  await expect(
    page.getByRole("heading", { name: "Allocation overview" }),
  ).toHaveCount(0);
  expect(errors).toEqual([]);
});
test("reject, reconnect, disconnect, back/forward and pagehide remove data", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByLabel("Admin read token").fill("b".repeat(64));
  await page.getByRole("button", { name: "Connect read-only" }).click();
  await expect(page.getByRole("alert")).toContainText("token rejected");
  await expect(page.getByLabel("Admin read token")).toHaveValue("");
  await connect(page);
  await page.getByRole("link", { name: "Hosts", exact: true }).click();
  await page.getByRole("button", { name: "Disconnect" }).click();
  await page.goBack();
  await page.goForward();
  await expect(
    page.getByRole("heading", { name: "Allocation overview" }),
  ).toHaveCount(0);
  await connect(page);
  await page.evaluate(() => window.dispatchEvent(new Event("pagehide")));
  await expect(page.getByLabel("Admin read token")).toHaveValue("");
  await expect(page.locator(".metric")).toHaveCount(0);
});
test("cancel interrupted login fences response and permits later reconnect", async ({
  page,
}) => {
  let release;
  let entered;
  const started = new Promise((resolve) => (entered = resolve));
  await page.route("**/admin/api/v1/overview", async (route) => {
    entered();
    await new Promise((resolve) => (release = resolve));
    try {
      await route.continue();
    } catch {}
  });
  await page.goto("/");
  await page.getByLabel("Admin read token").fill(token);
  await page.getByRole("button", { name: "Connect read-only" }).click();
  await started;
  await page.getByRole("button", { name: "Cancel / clear" }).click();
  release();
  await expect(page.getByLabel("Admin read token")).toHaveValue("");
  await expect(page.locator(".metric")).toHaveCount(0);
  await page.unroute("**/admin/api/v1/overview");
  await connect(page);
});
test("refresh failure removes stale private snapshot and returns to login", async ({
  page,
}) => {
  await connect(page);
  await page.route("**/admin/api/v1/overview", (route) =>
    route.fulfill({ status: 503, body: "unavailable" }),
  );
  await page.getByRole("button", { name: "Refresh snapshot" }).click();
  await expect(page.getByRole("alert")).toContainText("Disconnected");
  await expect(page.locator(".metric")).toHaveCount(0);
  await expect(page.getByLabel("Admin read token")).toHaveValue("");
});
test("mobile layout, no viewport overflow, actual data and empty credential form", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await connect(page);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
  await page.screenshot({
    path: "test-results/screenshots/mobile.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "Disconnect" }).click();
  await expect(page.getByLabel("Admin read token")).toHaveValue("");
  await page.screenshot({
    path: "test-results/screenshots/login-mobile.png",
    fullPage: true,
  });
});

test("review cancellation and separate write role persist actual policy with audit", async ({ page }) => {
  await connect(page);
  await page.getByLabel("New host mode").selectOption("disabled");
  await page.getByLabel("Audit reason").fill("browser fixture maintenance");
  await page.getByRole("button", { name: "Review change" }).click();
  await page.getByLabel("Management write token").fill("c".repeat(64));
  await page.getByRole("button", { name: "Cancel review" }).click();
  await expect(page.getByText("Configured mode: enabled")).toBeVisible();
  await page.getByLabel("Audit reason").fill("browser fixture maintenance");
  await page.getByRole("button", { name: "Review change" }).click();
  await expect(page.getByLabel("Management write token")).toHaveValue("");
  await page.getByLabel("Management write token").fill(token);
  await page.getByRole("button", { name: "Apply reviewed change" }).click();
  await expect(page.getByText("Management token rejected. The read token cannot authorize changes.")).toBeVisible();
  await expect(page.getByLabel("Management write token")).toHaveValue("");
  await page.getByLabel("Management write token").fill("c".repeat(64));
  await page.getByRole("button", { name: "Apply reviewed change" }).dblclick();
  await expect(page.getByText("Configured mode: disabled")).toBeVisible();
  await expect(page.getByText("admin_policy_change", { exact: true })).toHaveCount(1);
  await expect(page.getByText("Provisioning off · Admission barrier not ready")).toBeVisible();
  await page.getByLabel("Policy control").selectOption("pool_budget");
  await page.getByLabel("Budget runners").fill("0");
  await page.getByLabel("Audit reason").fill("stop future pool admissions");
  await page.getByRole("button", { name: "Review change" }).click();
  await page.getByLabel("Management write token").fill("c".repeat(64));
  await page.getByRole("button", { name: "Apply reviewed change" }).click();
  await expect(page.getByText("admin_policy_change", { exact: true })).toHaveCount(2);
  await expect(page.locator("#pools tbody tr")).toContainText("pool-a");
  expect(await page.evaluate(() => [localStorage.length, sessionStorage.length])).toEqual([0,0]);
  await page.getByRole("button", { name: "Disconnect" }).click();
  await connect(page);
  await expect(page.getByText("Configured mode: disabled")).toBeVisible();
  await expect(page.getByLabel("Management write token")).toHaveCount(0);
});

test("disconnect during an interrupted write never restores management state", async ({ page }) => {
  await connect(page);
  await page.getByLabel("Audit reason").fill("interrupted review");
  await page.getByRole("button", { name: "Review change" }).click();
  await page.getByLabel("Management write token").fill("c".repeat(64));
  let release, entered;
  const started = new Promise((resolve) => entered = resolve);
  await page.route("**/admin/api/v1/policy", async (route) => { entered(); await new Promise((resolve) => release=resolve); try { await route.continue(); } catch {} });
  await page.getByRole("button", { name: "Apply reviewed change" }).click();
  await started;
  await page.getByRole("button", { name: "Disconnect" }).click();
  release();
  await expect(page.getByLabel("Admin read token")).toHaveValue("");
  await expect(page.getByRole("heading", { name: "Ledger policy management" })).toHaveCount(0);
  await page.unroute("**/admin/api/v1/policy");
});

test("actual Garm settings use journalled typed writes and cancellation", async ({ page }) => {
  await connect(page);
  await page.getByRole("button", { name: "Load Garm settings" }).click();
  await expect(page.getByLabel("Pool priority")).toHaveValue("0");
  await page.getByLabel("Pool priority").fill("2");
  await page.getByLabel("Garm audit reason").fill("browser scoped priority change");
  await page.getByRole("button", { name: "Review Garm change" }).click();
  await page.getByLabel("Garm management write token").fill("c".repeat(64));
  await page.getByRole("button", { name: "Cancel Garm review" }).click();
  await page.getByLabel("Pool priority").fill("2");
  await page.getByLabel("Garm audit reason").fill("browser scoped priority change");
  await page.getByRole("button", { name: "Review Garm change" }).click();
  await expect(page.getByLabel("Garm management write token")).toHaveValue("");
  await page.getByLabel("Garm management write token").fill(token);
  await page.getByRole("button", { name: "Apply reviewed Garm change" }).click();
  await expect(page.locator("#garm-management")).toContainText("Management token rejected");
  await expect(page.getByLabel("Garm management write token")).toHaveValue("");
  await page.getByLabel("Garm management write token").fill("c".repeat(64));
  await page.getByRole("button", { name: "Apply reviewed Garm change" }).dblclick();
  await expect(page.getByLabel("Pool priority")).toHaveValue("2");
  await expect(page.locator(".garm-audit summary")).toHaveCount(1);
  const objectType = page.getByRole("combobox", { name: "Garm object type", exact: true });
  await expect(objectType).toHaveCount(1);
  await objectType.selectOption("repository");
  const balancer = page.getByRole("combobox", { name: "Repository balancing", exact: true });
  await expect(balancer).toHaveCount(1);
  await balancer.selectOption("pack");
  await page.getByLabel("Garm audit reason").fill("browser scoped balancing change");
  await page.getByRole("button", { name: "Review Garm change" }).click();
  await page.getByLabel("Garm management write token").fill("c".repeat(64));
  await page.getByRole("button", { name: "Apply reviewed Garm change" }).click();
  await expect(page.getByRole("combobox", { name: "Repository balancing", exact: true })).toHaveValue("pack");
  await expect(page.locator(".garm-audit summary")).toHaveCount(2);
  expect(await page.evaluate(() => [localStorage.length,sessionStorage.length])).toEqual([0,0]);
  await page.getByRole("button", { name: "Disconnect" }).click();
  await connect(page);
  await expect(page.getByLabel("Garm management write token")).toHaveCount(0);
  await page.getByRole("button", { name: "Load Garm settings" }).click();
  await expect(page.getByLabel("Pool priority")).toHaveValue("2");
});

test("ambiguous committed Garm write remains blocked after refresh and reconnect", async ({ page }) => {
  await connect(page);
  await page.getByRole("button", { name: "Load Garm settings" }).click();
  await page.getByLabel("Pool priority").fill("999");
  await page.getByLabel("Garm audit reason").fill("test-only committed500 fault");
  await page.getByRole("button", { name: "Review Garm change" }).click();
  await page.getByLabel("Garm management write token").fill("c".repeat(64));
  await page.getByRole("button", { name: "Apply reviewed Garm change" }).click();
  await expect(page.locator("#garm-management")).toContainText("outcome unavailable");
  await expect(page.getByRole("button", { name: "Review Garm change" })).toBeDisabled();
  await page.getByRole("button", { name: "Refresh Garm settings" }).click();
  await expect(page.getByLabel("Pool priority")).toHaveValue("999");
  await expect(page.locator("#garm-management")).toContainText("durable pending or unknown operation blocks Garm writes");
  await page.getByRole("button", { name: "Disconnect" }).click();
  await connect(page);
  await page.getByRole("button", { name: "Load Garm settings" }).click();
  await expect(page.getByRole("button", { name: "Review Garm change" })).toBeDisabled();
  await expect(page.locator(".garm-audit summary").first()).toContainText("unknown");
});

test("allowlisted Incus preflight is authenticated, audited, and never enrollment", async ({ page }) => {
  await connect(page);
  await page.getByRole("button", { name: "Load preflight profiles", exact: true }).click();
  await expect(page.getByRole("combobox", { name: "Allowlisted Incus profile", exact: true })).toHaveValue("candidate-b");
  await page.getByLabel("Preflight audit reason", { exact: true }).fill("browser onboarding read-only check");
  await page.getByLabel("Preflight management token", { exact: true }).fill(token);
  await page.getByRole("button", { name: "Run read-only preflight", exact: true }).click();
  await expect(page.locator("#onboarding-preflight")).toContainText("read token cannot authorize a probe");
  await expect(page.getByLabel("Preflight management token", { exact: true })).toHaveValue("");
  await page.getByLabel("Preflight management token", { exact: true }).fill("c".repeat(64));
  await page.getByRole("button", { name: "Run read-only preflight", exact: true }).dblclick();
  await expect(page.locator(".preflight-result")).toContainText("Authenticated daemon observation: available · version 6.0.4");
  await expect(page.locator(".preflight-result")).toContainText("No enrollment or activation occurred. Capacity remains unqualified.");
  await expect(page.locator("body")).not.toContainText("private-fixture-onboarding-daemon");
  await page.getByRole("button", { name: "Refresh snapshot", exact: true }).click();
  await expect(page.getByText("onboarding_probe_requested", { exact: true })).toHaveCount(1);
  await expect(page.getByText("onboarding_probe_completed", { exact: true })).toHaveCount(1);
  await expect(page.locator("#hosts h3")).toHaveCount(1);
  await expect(page.locator("#onboarding-preflight").getByRole("button", { name: /Enroll|Activate|Reset|Clear legacy/ })).toHaveCount(0);
  expect(await page.evaluate(() => [localStorage.length, sessionStorage.length])).toEqual([0,0]);
});

test("disconnect during preflight cannot restore a result or write credential", async ({ page }) => {
  await connect(page);
  await page.getByRole("button", { name: "Load preflight profiles", exact: true }).click();
  await page.getByLabel("Preflight audit reason", { exact: true }).fill("interrupted onboarding read");
  await page.getByLabel("Preflight management token", { exact: true }).fill("c".repeat(64));
  let release, entered;
  const started = new Promise((resolve) => entered = resolve);
  await page.route("**/admin/api/v1/onboarding/probe", async (route) => { entered(); await new Promise((resolve) => release = resolve); try { await route.continue(); } catch {} });
  await page.getByRole("button", { name: "Run read-only preflight", exact: true }).click();
  await started;
  await page.getByRole("button", { name: "Disconnect", exact: true }).click();
  release();
  await expect(page.getByLabel("Admin read token")).toHaveValue("");
  await expect(page.locator(".preflight-result")).toHaveCount(0);
  await expect(page.getByLabel("Preflight management token", { exact: true })).toHaveCount(0);
  await page.unroute("**/admin/api/v1/onboarding/probe");
  await connect(page);
  await page.getByRole("button", { name: "Load preflight profiles", exact: true }).click();
  await expect(page.getByLabel("Preflight management token", { exact: true })).toHaveValue("");
});

test("audit filters stay snapshot-local across clear, refresh and disconnect", async ({ page }) => {
  let refreshed = false;
  const events = [
    { sequence: 1, kind: "admitted", host: "host-a", request_key: "one", at: "2026-01-01T00:00:00Z" },
    { sequence: 2, kind: "denied", host: "host-b", request_key: "two", at: "2026-01-01T00:00:00Z" },
    { sequence: 3, kind: "denied", host: "", request_key: "three", at: "2026-01-01T00:00:00Z" },
  ];
  await page.route("**/admin/api/v1/overview", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({ response, json: { ...body, events: refreshed ? [] : events } });
  });
  const requests = [];
  page.on("request", request => requests.push(request.url()));
  await connect(page);
  const audit = page.locator("#events");
  await expect(audit.locator(".event .pill")).toHaveText(["#3", "#2", "#1"]);
  const initial = requests.length;
  await page.getByLabel("Event kind", { exact: true }).selectOption("denied");
  await page.getByLabel("Event host", { exact: true }).selectOption("host-a");
  await expect(audit).toContainText("No audit events match these filters.");
  await expect(audit).toContainText("Showing 0 of 3 events in this snapshot");
  await page.getByLabel("Event host", { exact: true }).selectOption("host-b");
  await expect(audit.locator(".event .pill")).toHaveText(["#2"]);
  await page.getByRole("button", { name: "Clear filters", exact: true }).click();
  await expect(audit.locator(".event .pill")).toHaveText(["#3", "#2", "#1"]);
  await expect(page.getByRole("button", { name: "Clear filters", exact: true })).toBeDisabled();
  expect(requests.length).toBe(initial);
  await page.getByLabel("Event host", { exact: true }).selectOption("host-b");
  refreshed = true;
  await page.getByRole("button", { name: "Refresh snapshot", exact: true }).click();
  await expect(audit).toContainText("Showing 0 of 0 events in this snapshot");
  await expect(audit).toContainText("No audit events recorded.");
  await expect(page.getByLabel("Event host", { exact: true })).toHaveValue("host-b");
  await expect(audit).toContainText("host-b (not in snapshot)");
  await page.getByRole("button", { name: "Disconnect", exact: true }).click();
  refreshed = false;
  await page.getByLabel("Admin read token").fill(token);
  await page.getByRole("button", { name: "Connect read-only", exact: true }).click();
  await expect(page.getByLabel("Event host", { exact: true })).toHaveValue("");
  await expect(audit.locator(".event")).toHaveCount(3);
});
