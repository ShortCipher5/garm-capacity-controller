import { validateAllocatorStatus } from "./allocator-status.js";
import { validateCapacityStates } from "./capacity-states.js";
import { validateBrokerView } from "./broker-policy-client.js";
import { validateOnboardingDiscovery, OnboardingResponseError } from "./onboarding-client.js";
import { validateGarm } from "./garm-client.js";
import { validateProfilesList } from "./profiles-client.js";
import { validateUsageHistory } from "./usage-client.js";

// This client has no persistence and no configurable remote endpoint.
export const endpoint = "/admin/api/v1/overview";
export class SessionEnded extends Error {}
export function validateOverview(value) {
  const object = (v) => v && typeof v === "object" && !Array.isArray(v);
  const integer = (v) => Number.isSafeInteger(v) && v >= 0;
  const resources = (v) =>
    object(v) &&
    ["cpu_millis", "ram_mib", "runners"].every((k) => integer(v[k]));
  const allocation = (v) =>
    object(v) && resources(v.budget) && resources(v.allocated);
  const time = (v) => typeof v === "string" && Number.isFinite(Date.parse(v));
  const resourcesFull = (v) =>
    object(v) &&
    ["cpu_millis", "ram_mib", "disk_mib", "runners"].every((k) => integer(v[k]));
  const detected = (v) => v === undefined || resourcesFull(v);
  const cpuCap = (v) => v === undefined || integer(v);
  const bool = (v) => typeof v === "boolean";
  const string = (v) => typeof v === "string";
  const id = (v) => string(v) && v.trim().length > 0;
  const uniqueIDs = (items) => new Set(items.map((item) => item.id)).size === items.length;
  if (
    !object(value) ||
    value.mode !== "experimental-ledger" ||
    !bool(value.management_enabled) ||
    typeof value.policy_revision !== "string" ||
    !/^[0-9a-f]{64}$/.test(value.policy_revision) ||
    !bool(value.experimental_provisioning_enabled) ||
    !bool(value.admission_ready) ||
    !integer(value.epoch) ||
    !integer(value.immutable_execution_plans) ||
    !integer(value.legacy_unplanned_reservations) ||
    !integer(value.legacy_source_preparations) ||
    !time(value.observed_at) ||
    !allocation(value.global) ||
    !Array.isArray(value.hosts) ||
    !value.hosts.every(
      (h) =>
        allocation(h) &&
        id(h.id) &&
        ["enabled", "draining", "disabled"].includes(h.mode) &&
        ["bound_observation", "legacy_scope_import_required", "unbound"].includes(h.backend_binding_status) &&
        (h.backend_binding_status === "bound_observation" || h.new_claims_paused === true) &&
        bool(h.last_observed_healthy) &&
        bool(h.observation_fresh) &&
        cpuCap(h.cpu_cap_millis) &&
        detected(h.detected) &&
        bool(h.new_claims_paused) &&
        (h.new_claims_paused || (h.mode === "enabled" && h.last_observed_healthy && h.observation_fresh && value.admission_ready && value.experimental_provisioning_enabled)) &&
        time(h.observed_at),
    ) ||
    !uniqueIDs(value.hosts) ||
    !Array.isArray(value.pools) ||
    !value.pools.every(
      (p) =>
        allocation(p) &&
        id(p.id) &&
        (p.hosts === null || (Array.isArray(p.hosts) && p.hosts.every(string))),
    ) ||
    !uniqueIDs(value.pools) ||
    !object(value.states) ||
    !Object.values(value.states).every(integer) ||
    !Array.isArray(value.events) ||
    !value.events.every(
      (e) =>
        object(e) &&
        integer(e.sequence) &&
        string(e.kind) &&
        time(e.at) &&
        (e.host === undefined || string(e.host)) &&
        (e.request_key === undefined || string(e.request_key)),
    )
  ) {
    throw new Error(
      "The server returned an unsupported overview. Disconnected.",
    );
  }
  return value;
}
export class ReadSession {
  #allocatorRequest;
  #allocatorGeneration=0;
  #token = "";
  #generation = 0;
  #request;
  #onboardingRequest;
  #onboardingGeneration=0;
  #entityRequest;
  #entityGeneration = 0;
  #remoteRequest;
  #remoteGeneration = 0;
  #capacityRequest;
  #capacityGeneration = 0;
  #brokerRequest;
  #brokerGeneration = 0;
  #profilesRequest;
  #profilesGeneration = 0;
  #usageInFlight = new Set();
  constructor(fetcher = globalThis.fetch) {
    // Native browser fetch requires the Window/global receiver, not this
    // ReadSession instance. Preserve it even when invoked as this.fetcher().
    this.fetcher = fetcher.bind(globalThis);
  }
  disconnect() {
    this.#allocatorGeneration++; this.#allocatorRequest?.abort(); this.#allocatorRequest=undefined;
    this.#entityGeneration++;
    this.#entityRequest?.abort();
    this.#entityRequest = undefined;
    this.#onboardingGeneration++;
    this.#onboardingRequest?.abort();
    this.#onboardingRequest=undefined;
    this.#generation++;
    this.#remoteGeneration++;
    this.#remoteRequest?.abort();
    this.#remoteRequest = undefined;
    this.#capacityGeneration++;
    this.#capacityRequest?.abort();
    this.#capacityRequest = undefined;
    this.#brokerGeneration++;
    this.#brokerRequest?.abort();
    this.#brokerRequest = undefined;
    this.#profilesGeneration++;
    this.#profilesRequest?.abort();
    this.#profilesRequest = undefined;
    for (const controller of this.#usageInFlight) controller.abort();
    this.#usageInFlight.clear();
    this.#token = "";
    this.#request?.abort();
    this.#request = undefined;
  }
  connect(token) {
    this.disconnect();
    if (!/^[0-9a-f]{64}$/.test(token))
      throw new Error(
        "Enter the 64-character lowercase hexadecimal read token.",
      );
    this.#token = token;
    return this.refresh();
  }
  async onboardingSnapshot() {
    if (!this.#token) throw new SessionEnded();
    this.#onboardingRequest?.abort();
    const generation = ++this.#onboardingGeneration;
    const controller = new AbortController(); this.#onboardingRequest = controller;
    const timeout = setTimeout(() => controller.abort(), 15000);
    try {
      const response = await this.fetcher("/admin/api/v1/onboarding", { method: "GET", headers: { Authorization: `Bearer ${this.#token}`, Accept: "application/json" }, credentials: "omit", cache: "no-store", redirect: "error", signal: controller.signal });
      if (generation !== this.#onboardingGeneration) throw new SessionEnded();
      if (controller.signal.aborted) throw new Error("Read observation expired.");
      if (response.status === 401) { this.disconnect(); throw new SessionEnded("Read authorization ended. Reconnect to the admin service."); }
      if (!response.ok) throw new OnboardingResponseError("Onboarding preflight profiles are unavailable.");
      const result = validateOnboardingDiscovery(await response.json());
      if (generation !== this.#onboardingGeneration) throw new SessionEnded();
      if (controller.signal.aborted) throw new Error("Read observation expired.");
      return result;
    } catch (error) {
      if (generation !== this.#onboardingGeneration && !(error instanceof SessionEnded)) throw new SessionEnded();
      if (error instanceof SessionEnded || error instanceof OnboardingResponseError) throw error;
      throw new OnboardingResponseError("Onboarding preflight discovery failed. No server was enrolled.");
    } finally { clearTimeout(timeout); }
  }
  async entityInventorySnapshot({signal} = {}) {
    if (!this.#token || signal?.aborted) throw new SessionEnded();
    this.#entityRequest?.abort();
    const generation = ++this.#entityGeneration;
    const controller = new AbortController(); this.#entityRequest = controller;
    const abort = () => controller.abort(); signal?.addEventListener("abort", abort, {once:true});
    const timeout = setTimeout(abort, 15000);
    try {
      const response = await this.fetcher("/admin/api/v1/garm", {method:"GET",headers:{Authorization:`Bearer ${this.#token}`,Accept:"application/json"},credentials:"omit",cache:"no-store",redirect:"error",signal:controller.signal});
      if (generation !== this.#entityGeneration || controller.signal.aborted) throw new SessionEnded();
      if (response.status === 401) { this.disconnect(); throw new SessionEnded("Read authorization ended. Reconnect to the admin service."); }
      if (!response.ok) throw new Error();
      const result = validateGarm(await response.json());
      if (generation !== this.#entityGeneration || controller.signal.aborted) throw new SessionEnded();
      return result;
    } catch(error) {
      if(error instanceof SessionEnded) throw error;
      throw new Error("Existing Garm scope observation unavailable.");
    } finally {clearTimeout(timeout);signal?.removeEventListener("abort",abort);}
  }
  async allocatorStatusSnapshot({signal}={}) {
    if(!this.#token) throw new SessionEnded();
    this.#allocatorRequest?.abort(); const generation=++this.#allocatorGeneration;
    const controller=new AbortController();this.#allocatorRequest=controller;
    const abort=()=>controller.abort();signal?.addEventListener('abort',abort,{once:true});if(signal?.aborted)abort();
    const timeout=setTimeout(abort,15000);
    try {
      const response=await this.fetcher('/admin/api/v1/allocator-status',{method:'GET',headers:{Authorization:`Bearer ${this.#token}`,Accept:'application/json'},credentials:'omit',cache:'no-store',redirect:'error',signal:controller.signal});
      if(generation!==this.#allocatorGeneration||controller.signal.aborted)throw new SessionEnded();
      if(response.status===401){this.disconnect();throw new SessionEnded('Read authorization ended. Reconnect to the admin service.');}
      if(!response.ok)throw new Error('Allocator status unavailable.');
      const result=validateAllocatorStatus(await response.json());
      if(generation!==this.#allocatorGeneration||controller.signal.aborted)throw new SessionEnded();
      return result;
    } finally {clearTimeout(timeout);signal?.removeEventListener('abort',abort);}
  }
  async garmSnapshot() {
    if (!this.#token) throw new SessionEnded();
    this.#remoteRequest?.abort();
    const generation = ++this.#remoteGeneration;
    const controller = new AbortController(); this.#remoteRequest = controller;
    const timeout = setTimeout(() => controller.abort(), 15000);
    try {
      const response = await this.fetcher("/admin/api/v1/garm", { method: "GET", headers: { Authorization: `Bearer ${this.#token}`, Accept: "application/json" }, credentials: "omit", cache: "no-store", redirect: "error", signal: controller.signal });
      if (generation !== this.#remoteGeneration) throw new SessionEnded();
      if (controller.signal.aborted) throw new Error("Read observation expired.");
      if (response.status === 401) { this.disconnect(); throw new SessionEnded("Read authorization ended. Reconnect to the admin service."); }
      if (!response.ok) throw new Error("Garm settings are unavailable. No settings were changed by this read.");
      const result = validateGarm(await response.json());
      if (generation !== this.#remoteGeneration) throw new SessionEnded();
      if (controller.signal.aborted) throw new Error("Read observation expired.");
      return result;
    } catch (error) {
      if (generation !== this.#remoteGeneration && !(error instanceof SessionEnded)) throw new SessionEnded();
      if (error instanceof SessionEnded || error.message?.includes("Garm")) throw error;
      throw new Error("Garm settings could not be read. Refresh before editing.");
    } finally { clearTimeout(timeout); }
  }
  async profilesSnapshot() {
    if (!this.#token) throw new SessionEnded();
    this.#profilesRequest?.abort();
    const generation = ++this.#profilesGeneration;
    const controller = new AbortController(); this.#profilesRequest = controller;
    const timeout = setTimeout(() => controller.abort(), 15000);
    try {
      const response = await this.fetcher("/admin/api/v1/profiles", { method: "GET", headers: { Authorization: `Bearer ${this.#token}`, Accept: "application/json" }, credentials: "omit", cache: "no-store", redirect: "error", signal: controller.signal });
      if (generation !== this.#profilesGeneration) throw new SessionEnded();
      if (controller.signal.aborted) throw new Error("Read observation expired.");
      if (response.status === 401) { this.disconnect(); throw new SessionEnded("Read authorization ended. Reconnect to the admin service."); }
      if (!response.ok) throw new Error("Runner-size profiles are unavailable. No fit estimates were rendered.");
      const result = validateProfilesList(await response.json());
      if (generation !== this.#profilesGeneration) throw new SessionEnded();
      if (controller.signal.aborted) throw new Error("Read observation expired.");
      return result;
    } catch (error) {
      if (generation !== this.#profilesGeneration && !(error instanceof SessionEnded)) throw new SessionEnded();
      if (error instanceof SessionEnded) throw error;
      throw new Error("Runner-size profiles could not be read. Refresh before reviewing fits.");
    } finally { clearTimeout(timeout); }
  }
  async brokerPolicySnapshot() {
    if (!this.#token) throw new SessionEnded();
    this.#brokerRequest?.abort();
    const generation = ++this.#brokerGeneration;
    const controller = new AbortController(); this.#brokerRequest = controller;
    const timeout = setTimeout(() => controller.abort(), 15000);
    try {
      const response = await this.fetcher("/admin/api/v1/broker-policy", { method: "GET", headers: { Authorization: `Bearer ${this.#token}`, Accept: "application/json" }, credentials: "omit", cache: "no-store", redirect: "error", signal: controller.signal });
      if (generation !== this.#brokerGeneration) throw new SessionEnded();
      if (controller.signal.aborted) throw new Error("Read observation expired.");
      if (response.status === 401) { this.disconnect(); throw new SessionEnded("Read authorization ended. Reconnect to the admin service."); }
      if (!response.ok) throw new Error("Advisory policies are unavailable. No policy was rendered.");
      const result = validateBrokerView(await response.json());
      if (generation !== this.#brokerGeneration) throw new SessionEnded();
      if (controller.signal.aborted) throw new Error("Read observation expired.");
      return result;
    } catch (error) {
      if (generation !== this.#brokerGeneration && !(error instanceof SessionEnded)) throw new SessionEnded();
      if (error instanceof SessionEnded) throw error;
      throw new Error("Advisory policies could not be read. Refresh before editing policy.");
    } finally { clearTimeout(timeout); }
  }
  async capacityStatesSnapshot() {
    if (!this.#token) throw new SessionEnded();
    this.#capacityRequest?.abort();
    const generation = ++this.#capacityGeneration;
    const controller = new AbortController(); this.#capacityRequest = controller;
    const timeout = setTimeout(() => controller.abort(), 15000);
    try {
      const response = await this.fetcher("/admin/api/v1/capacity-states", { method: "GET", headers: { Authorization: `Bearer ${this.#token}`, Accept: "application/json" }, credentials: "omit", cache: "no-store", redirect: "error", signal: controller.signal });
      if (generation !== this.#capacityGeneration) throw new SessionEnded();
      if (controller.signal.aborted) throw new Error("Read observation expired.");
      if (response.status === 401) { this.disconnect(); throw new SessionEnded("Read authorization ended. Reconnect to the admin service."); }
      if (!response.ok) throw new Error("Charged capacity is unavailable.");
      const result = validateCapacityStates(await response.json());
      if (generation !== this.#capacityGeneration) throw new SessionEnded();
      if (controller.signal.aborted) throw new Error("Read observation expired.");
      return result;
    } catch (error) {
      if (generation !== this.#capacityGeneration && !(error instanceof SessionEnded)) throw new SessionEnded();
      if (error instanceof SessionEnded) throw error;
      throw new Error("Charged capacity could not be read. Refresh to retry.");
    } finally { clearTimeout(timeout); }
  }
  async usageHistory(host, { since, limit, signal } = {}) {
    if (!this.#token) throw new SessionEnded();
    // Concurrent calls are independent: every host card owns its request.
    // A shared single-flight abort here would strand every card but the last
    // on 'Loading…', and a shared generation guard would turn concurrent
    // reads into fake session-ended errors.
    const controller = new AbortController();
    this.#usageInFlight.add(controller);
    const abort = () => controller.abort(); signal?.addEventListener("abort", abort, { once: true });
    const timeout = setTimeout(abort, 15000);
    const query = new URLSearchParams();
    if (since !== undefined) query.set("since", since instanceof Date ? since.toISOString() : String(since));
    if (limit !== undefined) query.set("limit", String(limit));
    const suffix = query.size ? `?${query}` : "";
    try {
      const response = await this.fetcher(`/admin/api/v1/usage/${encodeURIComponent(host)}${suffix}`, { method: "GET", headers: { Authorization: `Bearer ${this.#token}`, Accept: "application/json" }, credentials: "omit", cache: "no-store", redirect: "error", signal: controller.signal });
      if (controller.signal.aborted) throw new SessionEnded();
      if (response.status === 401) { this.disconnect(); throw new SessionEnded("Read authorization ended. Reconnect to the admin service."); }
      if (!response.ok) throw new Error("Usage history is unavailable. No automatic retry was made.");
      return validateUsageHistory(await response.json());
    } catch (error) {
      if (error instanceof SessionEnded) throw error;
      throw new Error("Usage history could not be read. Refresh before reviewing measured utilization.");
    } finally { clearTimeout(timeout); signal?.removeEventListener("abort", abort); this.#usageInFlight.delete(controller); }
  }
  async refresh() {
    if (!this.#token) throw new SessionEnded();
    this.#request?.abort();
    const generation = ++this.#generation;
    const controller = new AbortController();
    this.#request = controller;
    const timeout = setTimeout(() => controller.abort(), 15000);
    try {
      const response = await this.fetcher(endpoint, {
        method: "GET",
        headers: {
          Authorization: `Bearer ${this.#token}`,
          Accept: "application/json",
        },
        credentials: "omit",
        cache: "no-store",
        redirect: "error",
        signal: controller.signal,
      });
      if (generation !== this.#generation) throw new SessionEnded();
      if (controller.signal.aborted) throw new Error("Read observation expired.");
      if (response.status === 401)
        throw new Error(
          "Read token rejected or expired. Reconnect with a valid token.",
        );
      if (!response.ok)
        throw new Error(
          "The admin overview is unavailable. Disconnected; try reconnecting.",
        );
      const data = validateOverview(await response.json());
      if (generation !== this.#generation) throw new SessionEnded();
      if (controller.signal.aborted) throw new Error("Read observation expired.");
      return data;
    } catch (error) {
      if (generation !== this.#generation) throw new SessionEnded();
      this.disconnect();
      if (error instanceof SessionEnded) throw error;
      if (
        error.message?.includes("Disconnected") ||
        error.message?.includes("token rejected")
      )
        throw error;
      throw new Error(
        "The admin connection failed. Disconnected; check the service and reconnect.",
      );
    } finally {
      clearTimeout(timeout);
    }
  }
}

// One reviewed action only: no credential persistence, automatic retries,
// cookies, redirects, query tokens or configurable remote endpoint.
export async function applyPolicyChange(token, change, { signal, fetcher = globalThis.fetch } = {}) {
  if (!/^[0-9a-f]{64}$/.test(token)) throw new Error("Enter the 64-character lowercase hexadecimal management token.");
  let response;
  try {
    response = await fetcher.call(globalThis, "/admin/api/v1/policy", {
      method: "POST", headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json", Accept: "application/json" },
      body: JSON.stringify(change), credentials: "omit", cache: "no-store", redirect: "error", signal,
    });
  } catch {
    throw new Error("Change outcome unknown. Refresh policy and audit before attempting another change. No automatic retry was made.");
  }
  if (response.status === 409) throw new Error("Policy changed since review. Refresh the snapshot, cancel this review and review a new change.");
  if (response.status === 401) throw new Error("Management token rejected. The read token cannot authorize changes.");
  if (response.status === 400) throw new Error("Policy change rejected. No change was applied.");
  if (response.status === 404) throw new Error("Management is disabled on this server.");
  if (!response.ok) throw new Error("Change outcome unavailable. Refresh policy and audit before attempting another change. No automatic retry was made.");
  try {
    if ((await response.json()).applied !== true) throw new Error();
  } catch {
    throw new Error("Change confirmation unavailable. Refresh policy and audit before attempting another change. No automatic retry was made.");
  }
}
