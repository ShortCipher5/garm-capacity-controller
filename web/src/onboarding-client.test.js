import { test } from "node:test";
import assert from "node:assert/strict";
import { validateOnboardingDiscovery, validateOnboardingResult, probeOnboarding } from "./onboarding-client.js";
import { ReadSession, SessionEnded } from "./client.js";
const read = "a".repeat(64), write = "c".repeat(64);
const discovery = { mode: "read-only-onboarding-preflight", profiles: [{ id: "candidate-b" }], probe_enabled: true, enrollment_supported: false, capacity_qualified: false };
const result = { mode: "read-only-onboarding-preflight", request_id: "11111111-1111-4111-8111-111111111111", profile_id: "candidate-b", observed_at: "2026-01-01T00:00:00Z", observation_available: true, server_version: "6.0.4", stored_binding_class: "no_match_observed", enrollment_performed: false, activation_performed: false, capacity_qualified: false, blockers: ["enrollment_not_implemented", "physical_capacity_scope_unqualified"] };
const resources = { cpu_millis: 0, ram_mib: 0, runners: 0 };
const overview = { immutable_execution_plans: 0, legacy_unplanned_reservations: 0, legacy_source_preparations: 0, mode: "experimental-ledger", management_enabled: true, policy_revision: "f".repeat(64), experimental_provisioning_enabled: false, admission_ready: false, epoch: 0, observed_at: result.observed_at, global: { budget: resources, allocated: resources }, hosts: [], pools: [], events: [], states: {} };
test("preflight validators refuse fabricated enrollment/capacity and arbitrary profiles", () => {
  assert.equal(validateOnboardingDiscovery(discovery), discovery);
  assert.equal(validateOnboardingResult(result), result);
  for (const edit of [(value) => value.capacity_qualified = true, (value) => value.activation_performed = true, (value) => value.enrollment_performed = true, (value) => value.stored_binding_class = "hardware_qualified", (value) => value.server_version = "private\nmetadata", (value) => value.blockers = []]) {
    const value = structuredClone(result); edit(value); assert.throws(() => validateOnboardingResult(value), /Unsupported/);
  }
  assert.throws(() => validateOnboardingDiscovery({ ...discovery, profiles: [{ id: "https://unapproved.invalid" }] }), /Unsupported/);
  assert.throws(() => validateOnboardingDiscovery({ ...discovery, enrollment_supported: true }), /Unsupported/);
});
test("probe is one explicit same-origin no-cookie request and never sends endpoint/keys", async () => {
  let calls = 0;
  const request = { request_id: result.request_id, profile_id: result.profile_id, reason: "explicit read" };
  const fetcher = async function (url, options) {
    calls++; assert.equal(this, globalThis); assert.equal(url, "/admin/api/v1/onboarding/probe"); assert.equal(options.method, "POST");
    assert.equal(options.headers.Authorization, `Bearer ${write}`); assert.equal(options.credentials, "omit"); assert.equal(options.cache, "no-store"); assert.equal(options.redirect, "error"); assert.deepEqual(JSON.parse(options.body), request);
    return { ok: true, status: 200, json: async () => result };
  };
  assert.deepEqual(await probeOnboarding(write, request, { fetcher }), result); assert.equal(calls, 1);
  await assert.rejects(probeOnboarding("bad", request, { fetcher }), /management token/); assert.equal(calls, 1);
});
test("probe failures never retry or echo arbitrary transport/response errors", async () => {
  for (const status of [401, 400, 404, 409, 503]) {
    let calls = 0; await assert.rejects(probeOnboarding(write, {}, { fetcher: async () => { calls++; return { status, ok: false }; } })); assert.equal(calls, 1);
  }
  await assert.rejects(probeOnboarding(write, {}, { fetcher: async () => { throw Error("private preflight endpoint secret"); } }), (error) => !error.message.includes("secret"));
  await assert.rejects(probeOnboarding(write, {}, { fetcher: async () => ({ ok: true, status: 200, json: async () => { throw Error("private preflight response secret"); } }) }), (error) => !error.message.includes("secret"));
});
test("disconnect fences an ignored-abort profile read, without restoring credentials", async () => {
  let resolve;
  const session = new ReadSession(async (url, options) => {
    assert.equal(options.headers.Authorization, `Bearer ${read}`); assert.equal(options.credentials, "omit"); assert.equal(options.redirect, "error");
    if (url.endsWith("overview")) return { ok: true, status: 200, json: async () => overview };
    return new Promise((done) => resolve = done);
  });
  await session.connect(read); const pending = session.onboardingSnapshot(); session.disconnect(); resolve({ ok: true, status: 200, json: async () => discovery });
  await assert.rejects(pending, SessionEnded); await assert.rejects(session.onboardingSnapshot(), SessionEnded);
});
test("profile read failure cannot echo private JSON errors containing preflight", async () => {
  const session = new ReadSession(async (url) => ({ ok: true, status: 200, json: async () => { if (url.endsWith("overview")) return overview; throw Error("private preflight token secret"); } }));
  await session.connect(read); await assert.rejects(session.onboardingSnapshot(), (error) => !error.message.includes("secret")); session.disconnect();
});
test("probe rejects otherwise valid results for another request or profile", async () => {
  const request = { request_id: result.request_id, profile_id: result.profile_id, reason: "explicit read" };
  for (const changed of [{ ...result, profile_id: "other" }, { ...result, request_id: "22222222-2222-4222-8222-222222222222" }]) {
    let calls = 0;
    await assert.rejects(probeOnboarding(write, request, { fetcher: async () => { calls++; return { ok: true, status: 200, json: async () => changed }; } }), /Unsupported preflight result/);
    assert.equal(calls, 1);
  }
});
