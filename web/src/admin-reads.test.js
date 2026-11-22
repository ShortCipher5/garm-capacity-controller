import { test } from "node:test";
import assert from "node:assert/strict";
import { ReadSession, SessionEnded } from "./client.js";

const token = "a".repeat(64);
const listed = { profiles: [{ name: "small", cpu_millis: 2000, ram_mib: 4096, disk_mib: 0, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z", estimates: [{ host: "host-a", estimate: 1 }] }] };
const overview = {
  mode: "experimental-ledger",
  management_enabled: false,
  policy_revision: "f".repeat(64),
  experimental_provisioning_enabled: false,
  admission_ready: false,
  epoch: 0,
  immutable_execution_plans: 0,
  legacy_unplanned_reservations: 0,
  legacy_source_preparations: 0,
  observed_at: "2026-01-01T00:00:00Z",
  global: { budget: { cpu_millis: 0, ram_mib: 0, runners: 0 }, allocated: { cpu_millis: 0, ram_mib: 0, runners: 0 } },
  hosts: [],
  pools: [],
  states: {},
  events: [],
};
const overviewResponse = () => ({ ok: true, status: 200, json: async () => structuredClone(overview) });
test("profilesSnapshot reads the embedded estimates with the read credential", async () => {
  const calls = [];
  const session = new ReadSession(async (url, options) => {
    calls.push([url, options]);
    if (url === "/admin/api/v1/overview") return overviewResponse();
    return { ok: true, status: 200, json: async () => listed };
  });
  await session.connect(token);
  assert.deepEqual(await session.profilesSnapshot(), listed);
  const [url, options] = calls[1];
  assert.equal(url, "/admin/api/v1/profiles");
  assert.equal(options.headers.Authorization, `Bearer ${token}`);
  assert.equal(options.credentials, "omit");
  assert.equal(options.redirect, "error");
  session.disconnect();
  await assert.rejects(session.profilesSnapshot(), SessionEnded);
});
test("profilesSnapshot failure never keeps a stale private profile view", async () => {
  let calls = 0;
  const session = new ReadSession(async (url) => {
    calls++;
    if (url === "/admin/api/v1/overview") return overviewResponse();
    return { status: 401, ok: false };
  });
  await session.connect(token);
  await assert.rejects(session.profilesSnapshot(), SessionEnded);
  await assert.rejects(session.profilesSnapshot(), SessionEnded);
  assert.equal(calls, 2);
});
test("usageHistory passes since and limit and fences late bodies", async () => {
  const calls = [];
  const session = new ReadSession(async (url, options) => {
    calls.push([url, options]);
    if (url.startsWith("/admin/api/v1/overview")) return overviewResponse();
    return { ok: true, status: 200, json: async () => [] };
  });
  await session.connect(token);
  assert.deepEqual(await session.usageHistory("host a", { since: "2026-01-01T00:00:00Z", limit: 12 }), []);
  assert.equal(calls[1][0], `/admin/api/v1/usage/${encodeURIComponent("host a")}?since=2026-01-01T00%3A00%3A00Z&limit=12`);
  assert.deepEqual(await session.usageHistory("host-a"), []);
  assert.equal(calls[2][0], "/admin/api/v1/usage/host-a");
  const pending = session.usageHistory("host-a");
  session.disconnect();
  await assert.rejects(pending, SessionEnded);
});
test("usageHistory sends explicit recent order only for callers that ask", async () => {
  const calls = [];
  const session = new ReadSession(async (url, options) => {
    if (url === "/admin/api/v1/overview") return overviewResponse();
    calls.push([url, options.method]);
    return { ok: true, status: 200, json: async () => [] };
  });
  await session.connect(token);
  assert.deepEqual(await session.usageHistory("host-a", { limit: 500, order: "recent" }), []);
  assert.deepEqual(await session.usageHistory("host-a"), []);
  assert.deepEqual(calls, [
    ["/admin/api/v1/usage/host-a?limit=500&order=recent", "GET"],
    ["/admin/api/v1/usage/host-a", "GET"],
  ]);
});

test("usageHistory maps unavailable history without fabricating samples", async () => {
  const session = new ReadSession(async (url) => {
    if (url.startsWith("/admin/api/v1/overview")) return overviewResponse();
    return { status: 503, ok: false };
  });
  await session.connect(token);
  await assert.rejects(session.usageHistory("host-a"), /could not be read/);
  const malformed = new ReadSession(async (url) => {
    if (url.startsWith("/admin/api/v1/overview")) return overviewResponse();
    return { ok: true, status: 200, json: async () => ({}) };
  });
  await malformed.connect(token);
  await assert.rejects(malformed.usageHistory("host-a"), /could not be read/);
});

test("usageHistory sends no request for a pre-aborted caller", async () => {
  let requests = 0;
  const session = new ReadSession(async (url) => {
    if (url === "/admin/api/v1/overview") return overviewResponse();
    requests++;
    return { ok: true, status: 200, json: async () => [] };
  });
  await session.connect(token);
  const caller = new AbortController();
  caller.abort();
  await assert.rejects(session.usageHistory("host-a", { signal: caller.signal }), SessionEnded);
  assert.equal(requests, 0);
});

for (const stop of ["caller abort", "disconnect"]) {
  test(`usageHistory discards a delayed body after ${stop}`, async () => {
    const started = Promise.withResolvers();
    const body = Promise.withResolvers();
    const sample = {host: "host-a", sampled_at: "2026-01-01T00:00:00Z", source: "guest-state-v2",
      cpu_used_millis: 100, ram_used_mib: 200, disk_used_mib: 300};
    const session = new ReadSession(async (url, options) => {
      if (url === "/admin/api/v1/overview") return overviewResponse();
      assert.equal(url, "/admin/api/v1/usage/host-a");
      return { ok: true, status: 200, json: () => { started.resolve(options.signal); return body.promise; } };
    });
    await session.connect(token);
    const caller = new AbortController();
    const pending = session.usageHistory("host-a", { signal: caller.signal });
    const transportSignal = await started.promise;
    if (stop === "disconnect") session.disconnect();
    else caller.abort();
    assert.equal(transportSignal.aborted, true);
    body.resolve([sample]);
    await assert.rejects(pending, SessionEnded);
  });
}

test("usageHistory rejects an otherwise valid response containing another host", async () => {
  const mine = {host: "host-a", sampled_at: "2026-01-01T00:00:00Z", source: "guest-state-v2",
    cpu_used_millis: 100, ram_used_mib: 200, disk_used_mib: 300};
  const other = {...mine, host: "host-b"};
  const session = new ReadSession(async (url) => {
    if (url === "/admin/api/v1/overview") return overviewResponse();
    if (url === "/admin/api/v1/usage/host-b") return {ok: true, status: 200, json: async () => [other]};
    assert.equal(url, "/admin/api/v1/usage/host-a");
    return {ok: true, status: 200, json: async () => [mine, other]};
  });
  await session.connect(token);
  const rejected = assert.rejects(session.usageHistory("host-a"), /could not be read/);
  assert.deepEqual(await session.usageHistory("host-b"), [other]);
  await rejected;
});
