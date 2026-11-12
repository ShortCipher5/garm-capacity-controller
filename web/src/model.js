export function percent(allocated, budget) {
  if (!Number.isFinite(allocated) || !Number.isFinite(budget) || allocated < 0 || budget < 0) return null;
  if (budget === 0) return allocated === 0 ? 0 : 100;
  return Math.min(100, Math.round((allocated / budget) * 100));
}
export function validateOverview(data) {
  if (!data || data.mode !== 'simulation' || data.enforcement !== false) throw new Error('This demo only accepts simulation data.');
  if (!data.global || !Array.isArray(data.hosts) || !Array.isArray(data.pools) || !Array.isArray(data.events) || !Array.isArray(data.history)) throw new Error('Invalid demo response.');
  for (const item of [data.global, ...data.hosts]) {
    for (const key of ['cpu_millis', 'ram_mib', 'runners']) {
      if (!Number.isSafeInteger(item.budget?.[key]) || !Number.isSafeInteger(item.allocated?.[key]) || item.budget[key] < 0 || item.allocated[key] < 0) throw new Error('Invalid capacity response.');
    }
  }
  for (const host of data.hosts) {
    if (!['enabled', 'draining', 'disabled'].includes(host.mode) || typeof host.id !== 'string' || typeof host.name !== 'string') throw new Error('Invalid host response.');
  }
  for (const pool of data.pools) {
    if (typeof pool.repository !== 'string' || !Array.isArray(pool.labels) || !pool.labels.every(label => typeof label === 'string')) throw new Error('Invalid pool response.');
    for (const key of ['priority', 'max_runners', 'allocated', 'idle', 'running', 'unknown']) {
      if (!Number.isSafeInteger(pool[key]) || pool[key] < 0) throw new Error('Invalid pool accounting.');
    }
  }
  if (typeof data.observed_at !== 'string' || !Number.isFinite(Date.parse(data.observed_at))) throw new Error('Invalid observation time.');
  for (const event of data.events) {
    if (![event.time, event.decision, event.pool, event.reason].every(value => typeof value === 'string')) throw new Error('Invalid decision event.');
  }
  for (const point of data.history) {
    if (typeof point.time !== 'string' || !Number.isSafeInteger(point.runners) || point.runners < 0) throw new Error('Invalid history response.');
  }
  return data;
}
