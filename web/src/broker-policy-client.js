const uuid = v => typeof v === 'string' && v !== '00000000-0000-0000-0000-000000000000' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(v);
const count = v => Number.isSafeInteger(v) && v >= 0 && v <= 1000000;
export function validateBrokerPolicy(p) {
  if (!p || !Number.isSafeInteger(p.revision) || p.revision < 0 || p.revision >= Number.MAX_SAFE_INTEGER || !Array.isArray(p.repositories) || p.repositories.length > 1000) throw new Error('Invalid advisory policy.');
  const repos = new Set(), pools = new Set();
  for (const r of p.repositories) {
    if (!uuid(r.id) || repos.has(r.id) || !Number.isInteger(r.weight) || r.weight < 1 || r.weight > 4294967295 || !count(r.max_runners) || !Array.isArray(r.pools)) throw new Error('Invalid repository policy.');
    repos.add(r.id);
    for (const pool of r.pools) {
      if (!uuid(pool.id) || pools.has(pool.id) || !count(pool.max_runners) || pool.max_runners > r.max_runners || !count(pool.warm_idle) || pool.warm_idle > pool.max_runners) throw new Error('Pool IDs must be unique; idle target must fit pool cap, and pool cap must fit repository cap.');
      pools.add(pool.id);
      if (pools.size > 1000) throw new Error('Too many pools.');
    }
  }
  return p;
}
export function validateBrokerView(v) {
  if (!v || v.advisory_only !== true || typeof v.writes_enabled !== 'boolean') throw new Error('Unsupported advisory policy response.');
  validateBrokerPolicy(v.policy); return v;
}
export async function saveBrokerPolicy(token, policy, { signal, fetcher = globalThis.fetch } = {}) {
  validateBrokerPolicy(policy);
  if (!/^[0-9a-f]{64}$/.test(token)) throw new Error('Enter the separate 64-character management token.');
  let response;
  try { response = await fetcher('/admin/api/v1/broker-policy', { method:'PUT', headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/json'},body:JSON.stringify(policy),credentials:'omit',cache:'no-store',redirect:'error',signal }); }
  catch { throw new Error('Save outcome unknown. Reload policy before another edit. No automatic retry was made.'); }
  if (response.status === 409) throw new Error('Policy changed. Reload and review before saving.');
  if (!response.ok) throw new Error('Policy save not confirmed. Reload before another edit.');
  let result; try { result = await response.json(); } catch { throw new Error('Save outcome unknown. Reload policy.'); }
  if (result.saved !== true || result.advisory_only !== true || result.revision !== policy.revision + 1) throw new Error('Save confirmation unavailable. Reload policy.');
}
