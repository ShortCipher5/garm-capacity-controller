const hash = (v) => typeof v === "string" && /^[0-9a-f]{64}$/.test(v);
const object = (v) => v && typeof v === "object" && !Array.isArray(v);
const integer = (v) => Number.isSafeInteger(v) && v >= 0;
const string = (v) => typeof v === "string";
export function validateGarm(value) {
  if (!object(value) || !object(value.settings) || typeof value.settings.writes_enabled !== "boolean" || typeof value.history_truncated !== "boolean" || typeof value.settings_available !== "boolean" || typeof value.writes_enabled !== "boolean" || typeof value.write_blocked !== "boolean" ||
    !Array.isArray(value.history) || value.history.length>25 || !value.history.every((e)=>object(e)&&[e.request_id,e.kind,e.target,e.state,e.reason,e.created_at].every(string)&&["pending","unknown","confirmed","rejected"].includes(e.state)&&Number.isFinite(Date.parse(e.created_at))&&object(e.before)&&object(e.desired)&&(e.result===undefined||object(e.result))) ||
    !Array.isArray(value.settings.pools) || !Array.isArray(value.settings.repositories) ||
    !value.settings.pools.every((p) => object(p) && [p.id,p.provider_name].every(string) && hash(p.fingerprint) && typeof p.enabled === "boolean" && [p.priority,p.max_runners,p.min_idle_runners,p.generation].every(integer) && Array.isArray(p.tags) && p.tags.every(string)) ||
    !value.settings.repositories.every((r) => object(r) && [r.id,r.owner,r.name].every(string) && ["","pack","roundrobin"].includes(r.pool_balancing_type) && hash(r.fingerprint)) ||
    (value.pending !== undefined && (!object(value.pending) || ![value.pending.request_id,value.pending.kind,value.pending.target].every(string) || !Number.isFinite(Date.parse(value.pending.started_at))))) {
    throw new Error("The server returned unsupported Garm management settings.");
  }
  return value;
}
export async function applyGarmChange(token, kind, change, { signal, fetcher = globalThis.fetch } = {}) {
  if (!/^[0-9a-f]{64}$/.test(token)) throw new Error("Enter the separate 64-character management token.");
  if (!["pool","repository"].includes(kind)) throw new Error("Unsupported management action.");
  let response;
  try {
    response = await fetcher.call(globalThis, `/admin/api/v1/garm/${kind}`, { method: "POST", headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json", Accept: "application/json" }, body: JSON.stringify(change), credentials: "omit", cache: "no-store", redirect: "error", signal });
  } catch { throw new Error("Garm change outcome unknown. Refresh journal status. Do not submit another change. No automatic retry was made."); }
  if (response.status === 401) throw new Error("Management token rejected. The read token cannot authorize changes.");
  if (response.status === 409) throw new Error("Garm settings changed or another change is pending. Refresh settings and journal status before reviewing again.");
  if (response.status === 400) throw new Error("Unsupported or unsafe Garm change rejected.");
  if (response.status === 404) throw new Error("Garm writes are disabled on this server.");
  if (!response.ok) throw new Error("Garm change outcome unavailable. Refresh journal status. Do not retry the write.");
  try { if ((await response.json()).recorded_confirmed !== true) throw new Error(); }
  catch { throw new Error("Garm change confirmation unavailable. Refresh journal status. Do not retry the write."); }
}
