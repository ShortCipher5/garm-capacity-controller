const uuid = value => typeof value === "string" && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(value);
const fingerprint = value => typeof value === "string" && /^[0-9a-f]{64}$/.test(value);
const time = value => typeof value === "string" && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(value) && Number.isFinite(Date.parse(value));
const uint = (value, max) => Number.isSafeInteger(value) && value >= 0 && value <= max;
const text = value => typeof value === "string" && value.length > 0 && value.length <= 256 &&
  !/[\u0000-\u001f\u007f-\u009f]/.test(value) && new TextEncoder().encode(value).length <= 256;

function visiblePool(pool, target) {
  if (!pool || pool.id !== target || !uuid(pool.repo_id) || !text(pool.provider_name) || !fingerprint(pool.fingerprint) ||
      typeof pool.enabled !== "boolean" || !uint(pool.generation, Number.MAX_SAFE_INTEGER) ||
      ![pool.priority, pool.max_runners, pool.min_idle_runners].every(value => uint(value, 1000000)) ||
      pool.min_idle_runners > pool.max_runners || !Array.isArray(pool.tags) || pool.tags.length > 100 ||
      !pool.tags.every(text) || new Set(pool.tags).size !== pool.tags.length) return null;
  return {
    repository: pool.repo_id, provider: pool.provider_name, fingerprint: pool.fingerprint,
    settings: JSON.stringify([pool.enabled, pool.priority, pool.max_runners, pool.min_idle_runners, pool.generation, [...pool.tags].sort()]),
  };
}

// Compare only the visible settings for one exact unresolved repository-pool
// receipt. A late write or another writer can produce any of these readings.
// This must never classify dispatch, change an intent, or influence a control.
export function observePendingPoolSettings({ snapshot, unavailable = false, conflict = false } = {}) {
  // A retained draft can combine old settings and new history. Never compare it.
  if (unavailable || conflict || snapshot?.write_blocked !== true || snapshot.settings_available !== true ||
      (snapshot.maximum_reduction_only === true && snapshot.managed_history_available !== true)) return null;

  const pending = snapshot.pending;
  const history = snapshot.history;
  const pools = snapshot.settings?.pools;
  if (pending?.kind !== "pool" || !["pending", "unknown"].includes(pending.state) ||
      !uuid(pending.request_id) || !uuid(pending.target) || !time(pending.started_at) ||
      !Array.isArray(history) || history.length > 25 || !Array.isArray(pools) || pools.length > 1000) return null;
  const receipts = history.filter(event => event?.request_id === pending.request_id);
  const candidates = pools.filter(pool => pool?.id === pending.target);
  if (receipts.length !== 1 || candidates.length !== 1) return null;
  const receipt = receipts[0];
  if (receipt.kind !== "pool" || receipt.target !== pending.target || receipt.state !== pending.state ||
      receipt.created_at !== pending.started_at || receipt.result !== undefined) return null;

  const before = visiblePool(receipt.before, pending.target);
  const desired = visiblePool(receipt.desired, pending.target);
  const current = visiblePool(candidates[0], pending.target);
  // The request carries the prior fingerprint and cannot change generation.
  // Only the fresh read has its own fingerprint. Hidden metadata can change
  // it without changing the visible fields, but not the other way around.
  if (!before || !desired || !current || before.fingerprint !== desired.fingerprint || before.settings === desired.settings ||
      receipt.before.generation !== receipt.desired.generation ||
      before.repository !== desired.repository || before.repository !== current.repository ||
      before.provider !== desired.provider || before.provider !== current.provider ||
      (current.fingerprint === before.fingerprint && current.settings !== before.settings)) return null;
  if (current.settings === before.settings) return "matches-previous-visible";
  if (current.settings === desired.settings) return "matches-requested-visible";
  return "differs-from-both-visible";
}

export function pendingPoolObservationGuidance(observation) {
  const notices = {
    "matches-previous-visible": "The latest read matches the pool settings shown before this request.",
    "matches-requested-visible": "The latest read matches the visible pool settings requested.",
    "differs-from-both-visible": "The latest read differs from both the previous and requested visible pool settings.",
  };
  return Object.hasOwn(notices, observation) ? `${notices[observation]} This does not settle the write outcome or clear the block.` : "";
}
