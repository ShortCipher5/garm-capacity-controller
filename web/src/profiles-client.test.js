import { test } from "node:test";
import assert from "node:assert/strict";
import { validateProfilesList, validateProfileInput, fetchProfiles, upsertProfile, deleteProfile, ProfileResponseError } from "./profiles-client.js";

const token = "a".repeat(64);
const write = "c".repeat(64);
const listed = {
  profiles: [
    { name: "small", cpu_millis: 2000, ram_mib: 4096, disk_mib: 20480, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z", estimates: [{ host: "host-a", estimate: 1 }] },
    { name: "large", cpu_millis: 4000, ram_mib: 8192, disk_mib: 0, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-02T00:00:00Z", estimates: [{ host: "host-a", estimate: 0 }] },
  ],
};
test("profile list validation accepts the embedded estimates wire shape", () => {
  assert.deepEqual(validateProfilesList(listed), listed);
  assert.deepEqual(validateProfilesList({ profiles: [] }), { profiles: [] });
  for (const bad of [
    {},
    [],
    { profiles: [{ ...listed.profiles[0], name: "Bad" }] },
    { profiles: [{ ...listed.profiles[0], name: "bad_name" }] },
    { profiles: [{ ...listed.profiles[0], cpu_millis: 2500 }] },
    { profiles: [{ ...listed.profiles[0], cpu_millis: 0 }] },
    { profiles: [{ ...listed.profiles[0], ram_mib: 0 }] },
    { profiles: [{ ...listed.profiles[0], disk_mib: -1 }] },
    { profiles: [{ ...listed.profiles[0], estimates: [{ host: "host-a", estimate: -1 }] }] },
    { profiles: [{ ...listed.profiles[0], estimates: "none" }] },
    { profiles: [listed.profiles[0], listed.profiles[0]] },
  ]) assert.throws(() => validateProfilesList(bad), ProfileResponseError);
});
test("profile form input applies the store's durable rules", () => {
  assert.deepEqual(validateProfileInput({ name: "small-1", cpuVcpus: "2", ramMiB: "4096", diskMiB: "0" }), { name: "small-1", cpu_millis: 2000, ram_mib: 4096, disk_mib: 0 });
  for (const bad of [
    { name: "Bad", cpuVcpus: 2, ramMiB: 4096, diskMiB: 0 },
    { name: "", cpuVcpus: 2, ramMiB: 4096, diskMiB: 0 },
    { name: "small", cpuVcpus: 0, ramMiB: 4096, diskMiB: 0 },
    { name: "small", cpuVcpus: 1.5, ramMiB: 4096, diskMiB: 0 },
    { name: "small", cpuVcpus: 2, ramMiB: 0, diskMiB: 0 },
    { name: "small", cpuVcpus: 2, ramMiB: 4096, diskMiB: -1 },
  ]) assert.throws(() => validateProfileInput(bad), ProfileResponseError);
});
test("fetchProfiles uses the read credential on the exact same-origin GET", async () => {
  const calls = [];
  const result = await fetchProfiles(token, { fetcher: async (url, options) => { calls.push([url, options]); return { ok: true, status: 200, json: async () => listed }; } });
  assert.deepEqual(result, listed);
  assert.deepEqual(calls, [["/admin/api/v1/profiles", { method: "GET", headers: { Authorization: `Bearer ${token}`, Accept: "application/json" }, credentials: "omit", cache: "no-store", redirect: "error", signal: undefined }]]);
  await assert.rejects(fetchProfiles(token, { fetcher: async () => ({ status: 401, ok: false }) }), /Read token rejected/);
  await assert.rejects(fetchProfiles(token, { fetcher: async () => ({ status: 503, ok: false, json: async () => ({ error: "profile view unavailable; refresh before retrying" }) }) }), /profile view unavailable/);
  await assert.rejects(fetchProfiles(token, { fetcher: async () => { throw new Error("down"); } }), /No automatic retry/);
});
test("upsertProfile posts JSON with the write credential and maps durable outcomes", async () => {
  const calls = [];
  await upsertProfile(write, { name: "small", cpu_millis: 2000, ram_mib: 4096, disk_mib: 0 }, { fetcher: async (url, options) => { calls.push([url, options]); return { ok: true, status: 200, json: async () => ({ applied: true }) }; } });
  assert.equal(calls[0][0], "/admin/api/v1/profiles");
  assert.equal(calls[0][1].method, "POST");
  assert.equal(calls[0][1].headers.Authorization, `Bearer ${write}`);
  assert.equal(calls[0][1].headers["Content-Type"], "application/json");
  assert.equal(calls[0][1].body, JSON.stringify({ name: "small", cpu_millis: 2000, ram_mib: 4096, disk_mib: 0 }));
  await assert.rejects(upsertProfile(write, {}, { fetcher: async () => ({ status: 401, ok: false }) }), /Management token rejected/);
  await assert.rejects(upsertProfile(write, {}, { fetcher: async () => ({ status: 403, ok: false, json: async () => ({ error: "same origin required" }) }) }), /same origin required/);
  await assert.rejects(upsertProfile(write, {}, { fetcher: async () => ({ status: 404, ok: false, json: async () => ({ error: "profile management unavailable" }) }) }), /profile management unavailable/);
  await assert.rejects(upsertProfile(write, {}, { fetcher: async () => ({ status: 400, ok: false, json: async () => ({ error: "invalid runner-size profile" }) }) }), /invalid runner-size profile/);
});
test("deleteProfile deletes by name and renders not-found honestly", async () => {
  const calls = [];
  await deleteProfile(write, "small", { fetcher: async (url, options) => { calls.push([url, options]); return { ok: true, status: 200, json: async () => ({ deleted: true }) }; } });
  assert.equal(calls[0][0], "/admin/api/v1/profiles/small");
  assert.equal(calls[0][1].method, "DELETE");
  await assert.rejects(deleteProfile(write, "ghost", { fetcher: async () => ({ status: 404, ok: false, json: async () => ({ error: "runner-size profile unknown" }) }) }), /runner-size profile unknown/);
  await assert.rejects(deleteProfile(write, "Bad", { fetcher: async () => ({ status: 400, ok: false, json: async () => ({ error: "invalid runner-size profile name" }) }) }), /invalid runner-size profile name/);
  await assert.rejects(deleteProfile(write, "small", { fetcher: async () => ({ status: 401, ok: false }) }), /Management token rejected/);
});
