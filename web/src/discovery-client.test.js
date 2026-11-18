import { test } from "node:test";
import assert from "node:assert/strict";
import { validateDiscoveryResult, fetchProfileDiscovery, enrollHost, DiscoveryResponseError } from "./discovery-client.js";

const write = "c".repeat(64);
const result = {
  profile_id: "candidate-discover",
  observed_at: "2026-01-01T00:00:00Z",
  identity: { observation_available: false, stored_binding_class: "not_checked" },
  totals: { complete: true, cpu_millis: 8000, ram_mib: 32768, disk_mib: 524288, disk_mib_used: 131072 },
  guests: [
    { name: "runner-1", status: "Running", managed: true, controller: "ctl-1", pool: "pool-a", reservation: "res-1", cpu_millis: 2000, ram_mib: 4096, root_disk_mib: 20480 },
    { name: "sidecar", status: "Running", managed: false, cpu_millis: 500, ram_mib: 1024, root_disk_mib: 5120 },
  ],
  utilization: { source: "guest-state", cpu_cumulative_millis: 750, ram_used_mib: 2560, disk_used_mib: 25600, guests: 2 },
  suggested_budget: { cpu_millis: 6000, ram_mib: 30720, disk_mib: 471860, runners: 0 },
  warnings: ["point-in-time snapshot: observations go stale immediately and are not reconciliation evidence", "1 unmanaged guests: the shared limit may not cover them"],
  blockers: ["physical_capacity_scope_unqualified", "authenticated_identity_unavailable"],
};
test("discovery validation accepts the real wire shape and refuses inventions", () => {
  assert.deepEqual(validateDiscoveryResult(result), result);
  const minimal = { profile_id: "candidate-b", observed_at: "2026-01-01T00:00:00Z", identity: { observation_available: true, server_version: "6.0.4", stored_binding_class: "no_match_observed" }, guests: [], suggested_budget: { cpu_millis: 0, ram_mib: 0, disk_mib: 0, runners: 0 }, warnings: [], blockers: ["physical_capacity_scope_unqualified"] };
  assert.deepEqual(validateDiscoveryResult(minimal), minimal);
  for (const mutate of [
    (value) => delete value.blockers,
    (value) => (value.suggested_budget.cpu_millis = -1),
    (value) => (value.identity.stored_binding_class = "hardware_qualified"),
    (value) => (value.guests = [{ name: "x", status: "Running", managed: false, ram_mib: -1 }]),
    (value) => (value.totals = { complete: "yes", cpu_millis: 8000 }),
    (value) => (value.utilization = { source: "guest-state", cpu_cumulative_millis: "many" }),
    (value) => (value.warnings = "stale"),
  ]) {
    const changed = JSON.parse(JSON.stringify(result));
    mutate(changed);
    assert.throws(() => validateDiscoveryResult(changed), DiscoveryResponseError);
  }
});
test("fetchProfileDiscovery requires the management credential on the profile discovery route", async () => {
  const calls = [];
  const fetched = await fetchProfileDiscovery(write, "candidate-discover", { fetcher: async (url, options) => { calls.push([url, options]); return { ok: true, status: 200, json: async () => result }; } });
  assert.deepEqual(fetched, result);
  assert.equal(calls[0][0], "/admin/api/v1/onboarding/profiles/candidate-discover/discovery");
  assert.equal(calls[0][1].headers.Authorization, `Bearer ${write}`);
  await assert.rejects(fetchProfileDiscovery(write, "candidate-discover", { fetcher: async () => ({ status: 401, ok: false }) }), /Management token rejected/);
  await assert.rejects(fetchProfileDiscovery(write, "candidate-discover", { fetcher: async () => ({ status: 403, ok: false }) }), /Same origin required/);
  await assert.rejects(fetchProfileDiscovery(write, "unknown", { fetcher: async () => ({ status: 404, ok: false, json: async () => ({ error: "unknown onboarding profile" }) }) }), /unknown onboarding profile/);
  await assert.rejects(fetchProfileDiscovery(write, "candidate-discover", { fetcher: async () => ({ status: 409, ok: false, json: async () => ({ error: "another onboarding discovery is in progress" }) }) }), /in progress/);
  await assert.rejects(fetchProfileDiscovery(write, "candidate-discover", { fetcher: async () => { throw new Error("down"); } }), /No enrollment was performed/);
  await assert.rejects(fetchProfileDiscovery("bad", "candidate-discover"), DiscoveryResponseError);
});
test("enrollHost posts the reviewed enrollment and maps durable outcomes", async () => {
  const calls = [];
  const request = { name: "host-b", budget: { cpu_millis: 2000, ram_mib: 4096, disk_mib: 51200, runners: 2 }, review: { id: "11111111-1111-4111-8111-111111111111", revision: "a".repeat(64), profile_id: "candidate-b" }, mode: "disabled" };
  await enrollHost(write, request, { fetcher: async (url, options) => { calls.push([url, options]); return { ok: true, status: 201, json: async () => ({ enrolled: true }) }; } });
  assert.equal(calls[0][0], "/admin/api/v1/hosts");
  assert.equal(calls[0][1].method, "POST");
  assert.equal(calls[0][1].body, JSON.stringify(request));
  const omitted = await enrollHost(write, { name: "host-c", budget: request.budget, mode: "draining" }, { fetcher: async (url, options) => { assert.equal(options.body, JSON.stringify({ name: "host-c", budget: request.budget, mode: "draining" })); return { ok: true, status: 201, json: async () => ({}) }; } });
  assert.equal(omitted, undefined);
  await assert.rejects(enrollHost(write, request, { fetcher: async () => ({ status: 409, ok: false, json: async () => ({ error: "host already enrolled" }) }) }), /host already enrolled/);
  await assert.rejects(enrollHost(write, request, { fetcher: async () => ({ status: 400, ok: false, json: async () => ({ error: "invalid host enrollment" }) }) }), /invalid host enrollment/);
  await assert.rejects(enrollHost(write, request, { fetcher: async () => ({ status: 401, ok: false }) }), /Management token rejected/);
  await assert.rejects(enrollHost(write, { ...request, mode: "enabled" }, { fetcher: async () => ({ ok: true, status: 201 }) }), /disabled or draining/);
  await assert.rejects(enrollHost(write, { ...request, budget: { cpu_millis: -1, ram_mib: 1, disk_mib: 0, runners: 1 } }, { fetcher: async () => ({ ok: true, status: 201 }) }), /non-negative/);
});
test('discovery validates versioned server budget and saved size proposal', () => {
 const proposal={version:1,budget:{cpu_millis:1000,ram_mib:2048,disk_mib:10000},reserve:{cpu_millis:1000},warnings:[],sizes:[{name:'custom',cpu_millis:1000,ram_mib:2048,disk_mib:0}],size_source:'saved-profiles'};
 assert.deepEqual(validateDiscoveryResult({...result,proposal}).proposal,proposal);
 for(const change of [{version:2},{budget:{disk_mib:0}},{sizes:[{name:'custom',cpu_millis:0,ram_mib:1,disk_mib:1}]},{size_source:'qualified'}]) assert.throws(()=>validateDiscoveryResult({...result,proposal:{...proposal,...change}}),DiscoveryResponseError);
});
