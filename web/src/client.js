import { validateGarm } from "./garm-client.js";

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
  const bool = (v) => typeof v === "boolean";
  const string = (v) => typeof v === "string";
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
    !time(value.observed_at) ||
    !allocation(value.global) ||
    !Array.isArray(value.hosts) ||
    !value.hosts.every(
      (h) =>
        allocation(h) &&
        string(h.id) &&
        string(h.mode) &&
        ["bound_observation", "legacy_scope_import_required", "unbound"].includes(h.backend_binding_status) &&
        (h.backend_binding_status === "bound_observation" || h.new_claims_paused === true) &&
        bool(h.last_observed_healthy) &&
        bool(h.observation_fresh) &&
        bool(h.new_claims_paused) &&
        time(h.observed_at),
    ) ||
    !Array.isArray(value.pools) ||
    !value.pools.every(
      (p) =>
        allocation(p) &&
        string(p.id) &&
        (p.hosts === null || (Array.isArray(p.hosts) && p.hosts.every(string))),
    ) ||
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
  #token = "";
  #generation = 0;
  #request;
  #remoteRequest;
  #remoteGeneration = 0;
  constructor(fetcher = globalThis.fetch) {
    // Native browser fetch requires the Window/global receiver, not this
    // ReadSession instance. Preserve it even when invoked as this.fetcher().
    this.fetcher = fetcher.bind(globalThis);
  }
  disconnect() {
    this.#generation++;
    this.#remoteGeneration++;
    this.#remoteRequest?.abort();
    this.#remoteRequest = undefined;
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
  async garmSnapshot() {
    if (!this.#token) throw new SessionEnded();
    this.#remoteRequest?.abort();
    const generation = ++this.#remoteGeneration;
    const controller = new AbortController(); this.#remoteRequest = controller;
    const timeout = setTimeout(() => controller.abort(), 15000);
    try {
      const response = await this.fetcher("/admin/api/v1/garm", { method: "GET", headers: { Authorization: `Bearer ${this.#token}`, Accept: "application/json" }, credentials: "omit", cache: "no-store", redirect: "error", signal: controller.signal });
      if (generation !== this.#remoteGeneration) throw new SessionEnded();
      if (response.status === 401) { this.disconnect(); throw new SessionEnded("Read authorization ended. Reconnect to the admin service."); }
      if (!response.ok) throw new Error("Garm settings are unavailable. No settings were changed by this read.");
      const result = validateGarm(await response.json());
      if (generation !== this.#remoteGeneration) throw new SessionEnded();
      return result;
    } catch (error) {
      if (generation !== this.#remoteGeneration && !(error instanceof SessionEnded)) throw new SessionEnded();
      if (error instanceof SessionEnded || error.message?.includes("Garm")) throw error;
      throw new Error("Garm settings could not be read. Refresh before editing.");
    } finally { clearTimeout(timeout); }
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
