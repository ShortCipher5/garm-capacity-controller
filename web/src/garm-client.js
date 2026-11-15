const hash = (v) => typeof v === "string" && /^[0-9a-f]{64}$/.test(v);
const object = (v) => v && typeof v === "object" && !Array.isArray(v);
const integer = (v) => Number.isSafeInteger(v) && v >= 0;
const string = (v) => typeof v === "string";
// Older observations may omit both owner fields. Never infer ownership from
// pool/provider names; present that case as unknown, not an authorization.
function validInventoryOwner(pool) {
  if (pool.owner_kind === undefined) return pool.owner_id === undefined;
  if (pool.owner_kind === "unknown") return pool.owner_id === undefined;
  return ["repository", "organization", "enterprise"].includes(pool.owner_kind) &&
    typeof pool.owner_id === "string" && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(pool.owner_id);
}
function validInventoryTags(tags) {
  return Array.isArray(tags) && tags.length <= 100 && new Set(tags).size === tags.length &&
    tags.every(tag => typeof tag === "string" && tag.length > 0 &&
      new TextEncoder().encode(tag).length <= 256 && !/[\u0000-\u001f\u007f-\u009f]/.test(tag));
}
export function validateGarm(value) {
  if (!object(value) || !object(value.settings) || typeof value.settings.writes_enabled !== "boolean" || typeof value.history_truncated !== "boolean" || typeof value.settings_available !== "boolean" || typeof value.writes_enabled !== "boolean" || typeof value.write_blocked !== "boolean" ||
    !Array.isArray(value.history) || value.history.length>25 || !value.history.every((e)=>object(e)&&[e.request_id,e.kind,e.target,e.state,e.reason,e.created_at].every(string)&&["pending","unknown","confirmed","rejected"].includes(e.state)&&Number.isFinite(Date.parse(e.created_at))&&object(e.before)&&object(e.desired)&&(e.result===undefined||object(e.result))) ||
    !Array.isArray(value.settings.pools) || !Array.isArray(value.settings.repositories) ||
    !value.settings.pools.every((p) => object(p) && [p.id,p.provider_name].every(string) && hash(p.fingerprint) && typeof p.enabled === "boolean" && [p.priority,p.max_runners,p.min_idle_runners,p.generation].every(integer) && Array.isArray(p.tags) && p.tags.every(string)) ||
    !value.settings.repositories.every((r) => object(r) && [r.id,r.owner,r.name].every(string) && ["","pack","roundrobin"].includes(r.pool_balancing_type) && hash(r.fingerprint)) ||
    (value.pending !== undefined && (!object(value.pending) || ![value.pending.request_id,value.pending.kind,value.pending.target].every(string) || !Number.isFinite(Date.parse(value.pending.started_at))))) {
    throw new Error("The server returned unsupported Garm management settings.");
  }
  if(value.discovery_only!==undefined && (typeof value.discovery_only!=="boolean" || (value.discovery_only && (value.writes_enabled || value.settings.writes_enabled || value.settings_available || value.settings.pools.length || value.settings.repositories.length))))throw new Error("The server returned unsupported discovery-only state.");
  if(value.pool_inventory!==undefined){
    const inv=value.pool_inventory;
    if(!object(inv)||!Array.isArray(inv.pools)||inv.pools.length>10000||!Array.isArray(inv.missing_configured_pool_ids)||!inv.missing_configured_pool_ids.every(string)||new Set(inv.missing_configured_pool_ids).size!==inv.missing_configured_pool_ids.length||new Set(inv.pools.map(p=>p?.id)).size!==inv.pools.length||!inv.pools.every(p=>object(p)&&[p.id,p.provider_name].every(string)&&validInventoryOwner(p)&&p.fingerprint===""&&hash(p.discovery_fingerprint)&&typeof p.configured==="boolean"&&typeof p.enabled==="boolean"&&[p.priority,p.max_runners,p.min_idle_runners,p.generation].every(integer)&&p.min_idle_runners<=p.max_runners&&validInventoryTags(p.tags))||inv.missing_configured_pool_ids.some(id=>inv.pools.some(p=>p.id===id)))throw new Error("The server returned unsupported Garm pool inventory.");
  }
  if(value.scaleset_inventory!==undefined){
    const inv=value.scaleset_inventory;
    if(!object(inv)||!Array.isArray(inv.scalesets)||inv.scalesets.length>10000||new Set(inv.scalesets.map(s=>s?.id)).size!==inv.scalesets.length||!inv.scalesets.every(s=>object(s)&&integer(s.id)&&s.id>0&&[s.name,s.provider_name].every(v=>string(v)&&v.length>0)&&validInventoryOwner(s)&&hash(s.discovery_fingerprint)&&typeof s.enabled==="boolean"&&[s.max_runners,s.min_idle_runners,s.generation].every(integer)&&s.min_idle_runners<=s.max_runners&&["pending_create","created","error","pending_delete","pending_force_delete"].includes(s.state)&&validInventoryTags(s.tags)))throw new Error("The server returned unsupported Garm scaleset inventory.");
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
