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
    run_id: "9007199254740995", attempt: "2", observed_at: at, status: "unknown", jobs: [
      {id: "9007199254740997", status: "queued", steps: []},
      {id: "9007199254740998", status: "completed", steps: [
        {number: "7", status: "completed", execution: {basis: "observed_endpoints", nanoseconds: "2000000000"}},
      ]},
    ],
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
  const queued = panel.getByRole("table", {name: "Queued jobs (1 observed)"});
  await expect(queued).toContainText("9007199254740997");
  await expect(queued).toContainText("Unknown");
  const jobButton = queued.getByRole("button", {name: /^View job 9007199254740997, run 9007199254740995, attempt 2,/});
  const focusJob = page.locator(`[id="${await jobButton.getAttribute("aria-controls")}"]`);
  await expect(focusJob).toBeHidden(); // unknown attempt starts collapsed
  await jobButton.press("Enter");
  await expect(focusJob).toBeFocused();
  await expect(focusJob).toContainText("Job 9007199254740997");
  expect(calls).toBe(1);
  const longest = panel.getByRole("table", {name: "Longest measured steps in this snapshot"});
  await expect(longest).toContainText("Job 9007199254740998, step 7");
  await expect(longest).toContainText("2 s");
  const stepButton = longest.getByRole("button", {name: /^View step 7, job 9007199254740998,/});
  const focusTarget = page.locator(`[id="${await stepButton.getAttribute("aria-controls")}"]`);
  await stepButton.press("Enter");
  await expect(focusTarget).toBeFocused();
  expect(calls).toBe(1);

  status = 401;
  await panel.getByRole("button", {name: "Refresh timing snapshot"}).click();
  await expect(page.getByRole("heading", {name: "Connect to your ledger"})).toBeVisible();
  await expect(page.getByRole("alert")).toContainText("Read authorization ended");
  await expect(nav).toHaveCount(0);
  await expect(queued).toHaveCount(0);
  await expect(longest).toHaveCount(0);
  await expect(focusTarget).toHaveCount(0);
  await expect(focusJob).toHaveCount(0);
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
  const drill = panel.getByRole("button", {name: /^View step 7, job 9007199254740998,/});
  const focusedStep = page.locator(`[id="${await drill.getAttribute("aria-controls")}"]`);
  await drill.click();
  await expect(focusedStep).toBeFocused();
  // Advance monotonic time, then move the wall clock back without moving the
  // monotonic deadline. The (later) wall-only deadline must not keep rows alive.
  await page.clock.fastForward(60_000);
  await page.clock.setSystemTime(new Date(now.getTime() + 15_000));
  await page.clock.fastForward(31_000);
  await expect(panel).toContainText("The snapshot expired");
  await expect(panel).not.toContainText("9007199254740995");
  await expect(focusedStep).toHaveCount(0);
  expect(calls).toBe(2); // Expiry did not fetch or reschedule a read.

  await page.getByRole("button", {name: "Disconnect", exact: true}).click();
  await expect(panel).toHaveCount(0);
});

test("View step opens only the exact attempt and job, then resets with a replacement", async ({page}) => {
  let reads = 0;
  const observedAt = new Date().toISOString();
  await page.route(overviewRoute, (route) =>
    route.fulfill({json: {...overview, ci_health_available: true}}));
  await page.route(timingRoute, (route) => {
    reads++;
    const snapshot = timing(observedAt);
    const [retry] = snapshot.timing.attempts;
    const sibling = {...retry.jobs[1], id: "9007199254740999"};
    retry.jobs.push(sibling);
    snapshot.timing.attempts.unshift({...retry, attempt: "1", status: "completed", jobs: [
      {...sibling, id: "9007199254741000"},
    ]});
    return route.fulfill({json: snapshot});
  });
  await page.goto("/");
  await login(page);
  const panel = page.getByRole("region", {name: "CI Health", exact: true});
  await panel.getByRole("button", {name: "Load timing snapshot"}).click();
  const table = panel.getByRole("table", {name: "Longest measured steps in this snapshot"});
  const chosen = table.getByRole("button", {name: /^View step 7, job 9007199254740998, run 9007199254740995, attempt 2,/});
  const sibling = table.getByRole("button", {name: /^View step 7, job 9007199254740999, run 9007199254740995, attempt 2,/});
  const original = table.getByRole("button", {name: /^View step 7, job 9007199254741000, run 9007199254740995, attempt 1,/});
  const target = async (button) => page.locator(`[id="${await button.getAttribute("aria-controls")}"]`);
  const stateOf = (row) => row.evaluate((el) => ({
    attempt: el.closest("details[data-ci-attempt]").open,
    job: el.closest("details[data-ci-job-steps]").open,
  }));
  const firstChosen = await target(chosen);
  const firstSibling = await target(sibling);
  const oldButton = await chosen.elementHandle();
  const url = page.url();
  expect(await stateOf(firstChosen)).toEqual({attempt: false, job: false});
  await chosen.press("Enter");
  await expect(firstChosen).toBeFocused();
  await expect(firstChosen.getByRole("rowheader", {name: "Step 7"})).toBeVisible();
  expect(await stateOf(firstChosen)).toEqual({attempt: true, job: true});
  expect(await stateOf(firstSibling)).toEqual({attempt: true, job: false});
  expect(await stateOf(await target(original))).toEqual({attempt: false, job: false});
  expect(page.url()).toBe(url);
  expect(reads).toBe(1);

  // A script click at the refresh control doesn't itself move keyboard focus;
  // the replaced snapshot must discard the old row and its disclosure state.
  await panel.getByRole("button", {name: "Refresh timing snapshot"}).evaluate((el) => el.click());
  await expect(firstChosen).toHaveCount(0);
  const replacement = await target(chosen);
  expect(await stateOf(replacement)).toEqual({attempt: false, job: false});
  await expect(replacement).not.toBeFocused();
  await oldButton.evaluate((el) => el.click());
  expect(await stateOf(replacement)).toEqual({attempt: false, job: false});
  expect(reads).toBe(2);

  await chosen.press("Enter");
  await expect(replacement).toBeFocused();
  await page.getByRole("button", {name: "Disconnect", exact: true}).click();
  await expect(replacement).toHaveCount(0);
  expect(reads).toBe(2);
});

test("owner history expires independently without polling or losing fresh timing", async ({page}) => {
  const now = new Date();
  await page.clock.install({time: now});
  const at = now.getTime() - 20_000;
  const iso = (ms) => new Date(ms).toISOString();
  const week = 7 * 24 * 60 * 60 * 1000;
  let calls = 0;
  await page.route(overviewRoute, (route) =>
    route.fulfill({json: {...overview, ci_health_available: true}}));
  await page.route(timingRoute, (route) => {
    calls++;
    const snapshot = timing(iso(at));
    const attempt = snapshot.timing.attempts[0];
    attempt.status = "running";
    attempt.jobs[1].conclusion = "failure";
    attempt.jobs[1].steps[0].conclusion = "success";
    snapshot.coverage = "observed_subset";
    const {source, repository_id, workflow_id} = attempt;
    const metric = (outcome) => ({observed: outcome ? 1 : 0, success: Number(outcome === "success"),
      failure: Number(outcome === "failure"), other: 0, active: Number(outcome === "active"), unknown: 0});
    const bucket = () => ({distinct_runs: 0, attempts: metric(), jobs: metric(), steps: metric()});
    snapshot.history = {coverage: "retained_subset", test_results: "unavailable", expires_at: iso(at + 60_000),
      workflows: [{source, repository_id, workflow_id,
        previous: {from: iso(at - 2 * week), to: iso(at - week), initial: bucket(), retries: bucket()},
        current: {from: iso(at - week), to: iso(at), initial: bucket(),
          retries: {distinct_runs: 1, attempts: metric("active"), jobs: {...metric("failure"), observed: 2, active: 1},
            steps: {...metric("success"), duration: {samples: 1, mean_ns: "2000000000", p95_ns: "2000000000"}}}},
      }],
    };
    return route.fulfill({json: snapshot});
  });
  await page.goto("/");
  await login(page);
  const panel = page.getByRole("region", {name: "CI Health", exact: true});
  await panel.getByRole("button", {name: "Load timing snapshot"}).click();
  const history = panel.getByRole("region", {name: "Retained workflow timing and results"});
  await expect(history).toContainText("Verified test reports are unavailable");
  await history.locator("summary").click();
  await expect(history.getByRole("row").filter({hasText: "Jobs (pooled)"}).filter({hasText: "1/1 (100.0%)"})).toBeVisible();
  expect(calls).toBe(1);
  await page.clock.fastForward(25_000);
  await page.clock.setSystemTime(now);
  await page.clock.fastForward(16_000);
  await expect(history).toHaveCount(0);
  await expect(panel.getByRole("heading", {name: "Longest measured steps in this snapshot"})).toBeVisible();
  expect(calls).toBe(1);
});

test("resolves the exact job only on click, and discards a late destination after refresh", async ({page}) => {
  const handle = "A".repeat(43);
  const href = "https://github.com/synthetic/private-fixture/actions/runs/9007199254740995/job/9007199254740997";
  let requests = 0, external = 0, hold = true, release, mark;
  const pending = new Promise(resolve => { mark = resolve; });
  const gate = new Promise(resolve => { release = resolve; });
  await page.route(overviewRoute, (route) => route.fulfill({json: {...overview, ci_health_available: true}}));
  await page.route(timingRoute, async (route) => {
    const supplied = route.request().headers()["x-garm-ci-job-handle"];
    const payload = timing(new Date().toISOString());
    if (!supplied) {
      payload.timing.attempts[0].jobs[0].job_link_handle = handle;
      return route.fulfill({json: payload});
    }
    requests++;
    expect(supplied).toBe(handle);
    expect(route.request().headers().authorization).toBe("Bearer " + token);
    expect(route.request().url()).not.toContain("?");
    if (hold) {
      mark();
      await gate;
    }
    return route.fulfill({json: {url: href, source: "fixture", repository_id: "9007199254740993", workflow_id: "7",
      run_id: "9007199254740995", attempt: "2", job_id: "9007199254740997"}}).catch(() => {});
  });
  await page.route("https://github.com/**", (route) => {
    external++;
    expect(route.request().url()).toBe(href);
    expect(route.request().headers().authorization).toBeUndefined();
    expect(route.request().headers().referer).toBeUndefined();
    return route.fulfill({contentType: "text/html", body: "<p>Synthetic GitHub destination</p>"});
  });
  await page.goto("/");
  await login(page);
  const panel = page.getByRole("region", {name: "CI Health", exact: true});
  await panel.getByRole("button", {name: "Load timing snapshot"}).click();
  const reveal = panel.getByRole("button", {name: /^View job 9007199254740997,/});
  await reveal.click();
  const open = panel.getByRole("button", {name: "Open exact job on GitHub"});
  await expect(open).toBeVisible();
  await expect(panel.locator('a[href^="https://github.com/"]')).toHaveCount(0);
  expect(requests).toBe(0);
  await open.click();
  await pending;
  await panel.getByRole("button", {name: "Refresh timing snapshot"}).click();
  await reveal.click();
  hold = false;
  release();
  await expect(open).toBeVisible();
  expect(external).toBe(0);
  expect(requests).toBe(1);
  await open.click();
  await expect(page.getByText("Synthetic GitHub destination")).toBeVisible();
  expect(requests).toBe(2);
  expect(external).toBe(1);
});

test("measured disk and legacy snapshots display independently through producer changes", async ({page}) => {
  const asOf = new Date();
  const at = asOf.toISOString();
  const peak = new Date(asOf.getTime() - 6000).toISOString();
  const version = "disk-measured/v2";
  const gauge = {sample_peak_bytes: "1048576", gauge_samples: 3, peak_at: peak, latest_sample_at: at};
  const split = {
    state: "verified", version,
    read: {bytes: "0", covered_ns: "6000000000", complete: true},
    write: {bytes: "2048", covered_ns: "4000000000", complete: false},
    used: gauge,
  };
  const legacy = {state: "verified", read_bytes: "50", write_bytes: "3", covered_ns: "2000000000"};
  let disk = legacy;
  await page.route(overviewRoute, (route) =>
    route.fulfill({json: {...overview, ci_health_available: true}}));
  await page.route(timingRoute, (route) => {
    const snapshot = timing(at);
    snapshot.timing.attempts[0].jobs[1].resource_usage = {
      state: "partial", as_of: at, measured: {samples: 3, window_ns: "6000000000", disk},
    };
    return route.fulfill({json: snapshot});
  });
  await page.goto("/");
  await login(page);
  const panel = page.getByRole("region", {name: "CI Health", exact: true});
  const openJob = () => panel.getByRole("button", {name: /^View step 7, job 9007199254740998,/}).click();
  const refresh = async (nextDisk) => {
    disk = nextDisk;
    const response = page.waitForResponse((res) => res.url().endsWith("/admin/api/v1/ci-health"));
    await panel.getByRole("button", {name: "Refresh timing snapshot"}).click();
    await response;
  };

  await panel.getByRole("button", {name: "Load timing snapshot"}).click();
  await openJob();
  await expect(panel.getByText("50 B over 33% of the job window (partial coverage)", {exact: true})).toBeVisible();
  await expect(panel.getByText("3 B (coverage not recorded for this counter)", {exact: true})).toBeVisible();

  await refresh(split);
  await openJob();
  await expect(panel.getByText("0 B over 100% of the job window", {exact: true})).toBeVisible();
  await expect(panel.getByText("2 KiB over 66% of the job window (partial coverage)", {exact: true})).toBeVisible();
  await expect(panel.getByText(`sampled peak 1 MiB across 3 gauge samples, peak reached ${peak}, last gauge sample ${at}`, {exact: false})).toBeVisible();
  await expect(panel).not.toContainText(/(?:^|\s)0% of the job window/);

  await refresh({state: "verified", version, used: gauge});
  await openJob();
  await expect(panel.getByText("Disk used", {exact: true})).toBeVisible();
  await expect(panel.getByText("Disk read", {exact: true})).toHaveCount(0);
  await expect(panel.getByText("Disk written", {exact: true})).toHaveCount(0);
  await expect(panel).not.toContainText("% of the job window");

  await refresh(legacy);
  await openJob();
  await expect(panel.getByText("3 B (coverage not recorded for this counter)", {exact: true})).toBeVisible();
  await expect(panel.getByText("Disk used", {exact: true})).toHaveCount(0);

  await refresh({...split, read_bytes: "50"});
  await expect(panel).toContainText("CI Health timing is unavailable");
  await expect(panel.getByText("Disk written", {exact: true})).toHaveCount(0);
});
