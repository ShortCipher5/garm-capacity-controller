import { test } from "node:test";
import assert from "node:assert/strict";
import {
  ReadSession,
  SessionEnded,
  validateOverview,
  endpoint,
} from "./client.js";
const token = "a".repeat(64);
const resource = { cpu_millis: 0, ram_mib: 0, runners: 0 };
const view = {
  mode: "experimental-ledger",
  experimental_provisioning_enabled: false,
  admission_ready: false,
  epoch: 0,
  observed_at: "2026-01-01T00:00:00Z",
  global: { budget: resource, allocated: resource },
  hosts: [],
  pools: [],
  states: {},
  events: [],
};
const response = () => ({
  ok: true,
  status: 200,
  json: async () => structuredClone(view),
});
test("uses exact same-origin read-only no-cookie nonredirecting request", async () => {
  const session = new ReadSession(async (url, options) => {
    assert.equal(url, endpoint);
    assert.equal(options.method, "GET");
    assert.equal(options.headers.Authorization, `Bearer ${token}`);
    assert.equal(options.credentials, "omit");
    assert.equal(options.cache, "no-store");
    assert.equal(options.redirect, "error");
    return response();
  });
  assert.deepEqual(await session.connect(token), view);
  assert.deepEqual(await session.refresh(), view);
  session.disconnect();
  await assert.rejects(session.refresh(), SessionEnded);
});
test("invalid tokens never sent", async () => {
  const session = new ReadSession(() => assert.fail("must not fetch"));
  for (const invalid of ["", " a".repeat(64), "A".repeat(64), token + "?"])
    assert.throws(() => session.connect(invalid));
});
test("all failures erase read token and cannot silently retry", async () => {
  for (const fetcher of [
    async () => ({ ok: false, status: 401 }),
    async () => ({ ok: false, status: 503 }),
    async () => {
      throw new Error(token);
    },
    async () => ({ ok: true, status: 200, json: async () => ({}) }),
  ]) {
    const session = new ReadSession(fetcher);
    await assert.rejects(
      session.connect(token),
      (e) => !e.message.includes(token),
    );
    await assert.rejects(session.refresh(), SessionEnded);
  }
});
test("logout fences late responses even when transport ignores abort", async () => {
  let finish;
  const session = new ReadSession(
    () => new Promise((resolve) => (finish = resolve)),
  );
  const pending = session.connect(token);
  session.disconnect();
  finish(response());
  await assert.rejects(pending, SessionEnded);
  await assert.rejects(session.refresh(), SessionEnded);
});
test("new connection supersedes old request; stale failure cannot clear new token", async () => {
  let rejectOld;
  let calls = 0;
  const session = new ReadSession(() =>
    ++calls === 1
      ? new Promise((_, reject) => (rejectOld = reject))
      : Promise.resolve(response()),
  );
  const pending = session.connect(token);
  await session.connect("b".repeat(64));
  rejectOld(new Error("old failure"));
  await assert.rejects(pending, SessionEnded);
  assert.deepEqual(await session.refresh(), view);
});
test("rejects misleading or malformed contract instead of rendering invented values", () => {
  for (const changed of [
    { mode: "demo" },
    { global: {} },
    { epoch: -1 },
    { states: { idle: NaN } },
    { hosts: [{}] },
    { observed_at: "invalid" },
  ])
    assert.throws(() => validateOverview({ ...view, ...changed }));
});
test("disconnect during body decoding also fences private payload", async () => {
  let finishBody;
  const session = new ReadSession(async () => ({
    ok: true,
    status: 200,
    json: () => new Promise((resolve) => (finishBody = resolve)),
  }));
  const pending = session.connect(token);
  await Promise.resolve();
  session.disconnect();
  finishBody(view);
  await assert.rejects(pending, SessionEnded);
});
test("repeated refresh supersedes the earlier read without losing current session", async () => {
  let calls = 0,
    finishOld;
  const session = new ReadSession(async () =>
    ++calls === 2
      ? new Promise((resolve) => (finishOld = resolve))
      : response(),
  );
  await session.connect(token);
  const old = session.refresh();
  assert.deepEqual(await session.refresh(), view);
  finishOld(response());
  await assert.rejects(old, SessionEnded);
  assert.deepEqual(await session.refresh(), view);
});
