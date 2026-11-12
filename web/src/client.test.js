import { test } from "node:test";
import assert from "node:assert/strict";
import {
  ReadSession,
  SessionEnded,
  validateOverview,
  endpoint,
  applyPolicyChange,
} from "./client.js";
const token = "a".repeat(64);
const resource = { cpu_millis: 0, ram_mib: 0, runners: 0 };
const view = {
  mode: "experimental-ledger",
  management_enabled: false,
  policy_revision: "f".repeat(64),
  experimental_provisioning_enabled: false,
  admission_ready: false,
  epoch: 0,
  immutable_execution_plans: 0,
  legacy_unplanned_reservations: 0,
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
test("default native-style fetch retains its global receiver", async (t) => {
  let calls = 0;
  t.mock.method(globalThis, "fetch", async function (url, options) {
    // Browsers reject native fetch called with a ReadSession as its receiver.
    // Arrow-based Node/jsdom adapters do not reveal this integration failure.
    if (this !== globalThis) throw new TypeError("Illegal invocation");
    calls++;
    assert.equal(url, endpoint);
    assert.equal(options.credentials, "omit");
    assert.equal(options.headers.Authorization, `Bearer ${token}`);
    return response();
  });
  const session = new ReadSession();
  assert.deepEqual(await session.connect(token), view);
  assert.deepEqual(await session.refresh(), view);
  assert.equal(calls, 2);
  session.disconnect();
});

test("management action uses distinct one-shot same-origin write contract", async () => {
  const change = { revision: "f".repeat(64), action: "host_mode", target: "host-a", mode: "disabled", reason: "test" };
  let calls = 0;
  const fetcher = async function(url, options) {
    calls++; assert.equal(this, globalThis); assert.equal(url, "/admin/api/v1/policy");
    assert.equal(options.method, "POST"); assert.equal(options.headers.Authorization, `Bearer ${"c".repeat(64)}`);
    assert.equal(options.headers["Content-Type"], "application/json");
    assert.equal(options.credentials, "omit"); assert.equal(options.cache, "no-store"); assert.equal(options.redirect, "error");
    assert.deepEqual(JSON.parse(options.body), change); return { ok: true, status: 200, json: async () => ({ applied: true }) };
  };
  await applyPolicyChange("c".repeat(64), change, { fetcher }); assert.equal(calls, 1);
  await assert.rejects(applyPolicyChange("bad", change, { fetcher }), /management token/); assert.equal(calls, 1);
});
test("management conflicts and ambiguous writes never automatically retry", async () => {
  for (const [status, message] of [[409,/Policy changed/],[401,/read token cannot/],[503,/outcome unavailable/]]) {
    let calls=0; await assert.rejects(applyPolicyChange("c".repeat(64), {}, { fetcher: async()=> {calls++; return {status,ok:false};} }), message); assert.equal(calls,1);
  }
  let calls=0; await assert.rejects(applyPolicyChange("c".repeat(64), {}, { fetcher: async()=>{calls++;throw Error("secret transport message");} }), /outcome unknown/); assert.equal(calls,1);
});

test("host binding classification must be explicit, never hardware qualification", () => {
  const host={id:"host-a",mode:"enabled",last_observed_healthy:true,observation_fresh:true,new_claims_paused:true,observed_at:view.observed_at,budget:resource,allocated:resource};
  for(const status of ["bound_observation","legacy_scope_import_required","unbound"]){assert.equal(validateOverview({...view,hosts:[{...host,backend_binding_status:status}]}).hosts[0].backend_binding_status,status);}
  for(const status of [undefined,"verified","hardware_qualified"]){assert.throws(()=>validateOverview({...view,hosts:[{...host,backend_binding_status:status}]}),/unsupported overview/);}
});

test("unbound or legacy status cannot advertise new claims allowed",()=>{
 const host={id:"host-a",mode:"enabled",last_observed_healthy:true,observation_fresh:true,new_claims_paused:false,observed_at:view.observed_at,budget:resource,allocated:resource};
 for(const backend_binding_status of ["unbound","legacy_scope_import_required"]){assert.throws(()=>validateOverview({...view,hosts:[{...host,backend_binding_status}]}),/unsupported overview/);}
});

test("immutable execution-plan counters are explicit and cannot invent migration", () => {
 for(const field of ["immutable_execution_plans","legacy_unplanned_reservations"]){for(const invalid of [undefined,-1,NaN,"0"]){const candidate=structuredClone(view);candidate[field]=invalid;assert.throws(()=>validateOverview(candidate));}}
});
