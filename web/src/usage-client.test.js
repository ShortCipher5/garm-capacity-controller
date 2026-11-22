import { test } from "node:test";
import assert from "node:assert/strict";
import { validateUsageHistory, fetchUsageHistory, usageScope, UsageResponseError } from "./usage-client.js";

const token = "a".repeat(64);
const sample = (at, cpu, ram = 1024, disk = 2048) => ({ host: "host-a", sampled_at: at, cpu_used_millis: cpu, ram_used_mib: ram, disk_used_mib: disk, source: "fixture-collector" });

test("usage history validation accepts the wire shape and rejects inventions", () => {
  assert.deepEqual(validateUsageHistory([sample("2026-01-01T00:00:00Z", 100)]), [sample("2026-01-01T00:00:00Z", 100)]);
  assert.deepEqual(validateUsageHistory([]), []);
  for (const bad of [
    {},
    [null],
    [{ ...sample("2026-01-01T00:00:00Z", 100), cpu_used_millis: -1 }],
    [{ ...sample("2026-01-01T00:00:00Z", 100), ram_used_mib: 1.5 }],
    [{ ...sample("2026-01-01T00:00:00Z", 100), sampled_at: "not a time" }],
    [{ ...sample("2026-01-01T00:00:00Z", 100), source: "" }],
    [{ ...sample("2026-01-01T00:00:00Z", 100), host: "" }],
  ]) assert.throws(() => validateUsageHistory(bad), UsageResponseError);
});
test("fetchUsageHistory issues the exact same-origin read request", async () => {
  const calls = [];
  const history = [{ ...sample("2026-01-01T00:00:00Z", 100), host: "host a" }];
  const result = await fetchUsageHistory(token, "host a", {
    since: "2026-01-01T00:00:00Z",
    limit: 12,
    fetcher: async (url, options) => {
      calls.push([url, options]);
      return { ok: true, status: 200, json: async () => history };
    },
  });
  assert.deepEqual(result, history);
  assert.equal(calls.length, 1);
  const [url, options] = calls[0];
  assert.equal(url, `/admin/api/v1/usage/${encodeURIComponent("host a")}?since=2026-01-01T00%3A00%3A00Z&limit=12`);
  assert.equal(options.method, "GET");
  assert.equal(options.headers.Authorization, `Bearer ${token}`);
  assert.equal(options.credentials, "omit");
  assert.equal(options.cache, "no-store");
  assert.equal(options.redirect, "error");
  const plain = await fetchUsageHistory(token, "host-a", { fetcher: async (plainUrl) => { assert.equal(plainUrl, "/admin/api/v1/usage/host-a"); return { ok: true, status: 200, json: async () => [] }; } });
  assert.deepEqual(plain, []);
});
test("fetchUsageHistory renders auth and server errors honestly", async () => {
  await assert.rejects(fetchUsageHistory(token, "host-a", { fetcher: async () => ({ status: 401, ok: false }) }), /Read token rejected/);
  await assert.rejects(fetchUsageHistory(token, "host-a", { fetcher: async () => ({ status: 400, ok: false, json: async () => ({ error: "invalid since" }) }) }), /invalid since/);
  await assert.rejects(fetchUsageHistory(token, "host-a", { fetcher: async () => ({ status: 503, ok: false, json: async () => ({ error: "usage history unavailable" }) }) }), /usage history unavailable/);
  await assert.rejects(fetchUsageHistory(token, "host-a", { fetcher: async () => ({ status: 500, ok: false, json: async () => ({}) }) }), /unavailable/);
  await assert.rejects(fetchUsageHistory(token, "host-a", { fetcher: async () => { throw new Error("down"); } }), /No automatic retry/);
  await assert.rejects(fetchUsageHistory(token, "host-a", { fetcher: async () => ({ ok: true, status: 200, json: async () => ({}) }) }), /Unsupported usage history/);
  await assert.rejects(fetchUsageHistory("nope", "host-a"), UsageResponseError);
});

test("guest-state scope is project guest observations, never host load", () => {
  const guest = {...sample("2026-01-01T00:00:00Z", 100), source: "guest-state"};
  assert.match(usageScope([guest]), /Project guest observations, not whole-host load/);
  assert.match(usageScope([guest]), /do not establish physical host pressure or headroom/);
  assert.match(usageScope([guest]), /stored zero in legacy history is not proof of zero usage/);
  const current = {...guest, source: "guest-state-v2"};
  assert.match(usageScope([current]), /Project guest observations, not whole-host load/);
  assert.doesNotMatch(usageScope([current]), /legacy/);
  assert.match(usageScope([guest, current]), /stored zero in legacy history/);
  assert.match(usageScope([guest, {...guest, source:"other"}]), /Source-specific/);
  assert.match(usageScope([]), /unavailable/);
});

test("fetchUsageHistory rejects a host mismatch instead of presenting it as the requested host", async () => {
  await assert.rejects(fetchUsageHistory(token, "host-b", {
    fetcher: async () => ({ok: true, status: 200, json: async () => [sample("2026-01-01T00:00:00Z", 100)]}),
  }), UsageResponseError);
});

test("usage history rejects coerced, non-Go, and impossible wire timestamps", () => {
  for (const at of [
    undefined, null, 0, 2026, ["2026-01-01T00:00:00Z"], {}, true,
    "2026", "2026-01-01", "2026-01-01 00:00:00Z", "2026-01-01T00:00:00",
    "2026-01-01T00:00:00+0800", "2026-01-01T00:00:00z", "2026-01-01t00:00:00Z",
    "2026-01-01T00:00:00Z\n", " 2026-01-01T00:00:00Z",
    "2026-02-29T00:00:00Z", "1900-02-29T00:00:00Z", "2026-04-31T12:00:00+08:00",
    "2026-00-01T00:00:00Z", "2026-13-01T00:00:00Z", "2026-01-00T00:00:00Z",
    "2026-01-01T24:00:00Z", "2026-01-01T23:60:00Z", "2026-01-01T23:59:60Z",
    "2026-01-01T00:00:00+24:00", "2026-01-01T00:00:00-01:60",
    "2026-01-01T00:00:00.1234567890Z", "2026-01-01T00:00:00.Z",
  ]) {
    assert.throws(() => validateUsageHistory([sample(at, 100)], "host-a"), UsageResponseError, JSON.stringify(at));
  }
});

test("usage history preserves Go RFC3339Nano strings, offsets and empty samples", () => {
  const dates = [
    "2024-02-29T00:00:00Z", "2000-02-29T23:59:59.123456789-03:30",
    "0000-02-29T00:00:00+00:00", "9999-12-31T00:00:00-00:00",
    ...Array.from({length: 9}, (_, i) => `2026-01-01T00:00:00.${"123456789".slice(0, i + 1)}+08:00`),
  ];
  const history = dates.map(at => sample(at, 100));
  assert.strictEqual(validateUsageHistory(history, "host-a"), history);
  assert.deepEqual(validateUsageHistory([], "host-a"), []);
});
