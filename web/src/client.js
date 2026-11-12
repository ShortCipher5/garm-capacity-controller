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
    !bool(value.experimental_provisioning_enabled) ||
    !bool(value.admission_ready) ||
    !integer(value.epoch) ||
    !time(value.observed_at) ||
    !allocation(value.global) ||
    !Array.isArray(value.hosts) ||
    !value.hosts.every(
      (h) =>
        allocation(h) &&
        string(h.id) &&
        string(h.mode) &&
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
  constructor(fetcher = globalThis.fetch) {
    this.fetcher = fetcher;
  }
  disconnect() {
    this.#generation++;
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
