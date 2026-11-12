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
