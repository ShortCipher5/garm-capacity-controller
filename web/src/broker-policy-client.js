import {validateBrokerStatus} from "./broker-status.js";
const uuid = v => typeof v === 'string' && v !== '00000000-0000-0000-0000-000000000000' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(v);
const count = v => Number.isSafeInteger(v) && v >= 0 && v <= 1000000;
export function validateBrokerPolicy(p) {
  if (!p || !Number.isSafeInteger(p.revision) || p.revision < 0 || p.revision >= Number.MAX_SAFE_INTEGER || !Array.isArray(p.repositories) || p.repositories.length > 1000) throw new Error('Invalid advisory policy.');
  const repos = new Set(), pools = new Set();
  for (const [i, r] of p.repositories.entries()) {
    const at = `Invalid repository policy (row ${i + 1})`;
    if (!r || !uuid(r.id)) throw new Error(`${at}: enter a nonzero lowercase GARM repository UUID.`);
    if (repos.has(r.id)) throw new Error(`${at}: repository UUID is already used in this draft.`);
    if (!Number.isInteger(r.weight) || r.weight < 1 || r.weight > 4294967295) throw new Error(`${at}: fairness weight must be a whole number from 1 to 4294967295.`);
    if (!count(r.max_runners)) throw new Error(`${at}: runner cap must be a whole number from 0 to 1000000.`);
    if (!Array.isArray(r.pools)) throw new Error(`${at}: pool list is missing or invalid.`);
    repos.add(r.id);
    for (const [j, pool] of r.pools.entries()) {
      const poolAt = `Repository row ${i + 1}, pool row ${j + 1}`;
      if (!pool || !uuid(pool.id)) throw new Error(`${poolAt}: enter a nonzero lowercase GARM pool UUID.`);
      if (pools.has(pool.id)) throw new Error(`${poolAt}: pool UUID is already used in this draft.`);
      if (!count(pool.max_runners) || pool.max_runners > r.max_runners) throw new Error(`${poolAt}: runner cap must be a whole number from zero to the repository cap.`);
      if (!count(pool.warm_idle) || pool.warm_idle > pool.max_runners) throw new Error(`${poolAt}: warm idle target must be a whole number from zero to the pool cap.`);
      pools.add(pool.id);
      if (pools.size > 1000) throw new Error('Too many pools.');
    }
  }
  return p;
}
export function validateBrokerView(v) {
  if (!v || v.advisory_only !== true || typeof v.writes_enabled !== 'boolean') throw new Error('Unsupported advisory policy response.');
  validateBrokerPolicy(v.policy); if(v.status!==undefined)validateBrokerStatus(v.status,v.policy); return v;
}
export function validateBrokerSaveInput(token, policy) {
  validateBrokerPolicy(policy);
  if (!/^[0-9a-f]{64}$/.test(token)) throw new Error('Enter the separate 64-character management token.');
}
export async function saveBrokerPolicy(token, policy, { signal, fetcher = globalThis.fetch } = {}) {
  validateBrokerSaveInput(token, policy);
  let response;
  try { response = await fetcher('/admin/api/v1/broker-policy', { method:'PUT', headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/json'},body:JSON.stringify(policy),credentials:'omit',cache:'no-store',redirect:'error',signal }); }
  catch { throw new Error('Save outcome unknown. Reload policy before another edit. No automatic retry was made.'); }
  if (response.status === 409) throw new Error('Policy changed. Reload and review before saving.');
  if (!response.ok) throw new Error('Policy save not confirmed. Reload before another edit.');
  let result; try { result = await response.json(); } catch { throw new Error('Save outcome unknown. Reload policy.'); }
  if (result.saved !== true || result.advisory_only !== true || result.revision !== policy.revision + 1) throw new Error('Save confirmation unavailable. Reload policy.');
}
