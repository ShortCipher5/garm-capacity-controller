// Advisory planning only. These calculations never authorize a host or release
// an existing allocation. Observed guests are conservatively reserved in full.
export const RUNNER_PRESETS = [
  { name: 'small', cpu_millis: 1000, ram_mib: 2048, disk_mib: 20480 },
  { name: 'medium', cpu_millis: 2000, ram_mib: 4096, disk_mib: 40960 },
  { name: 'large', cpu_millis: 4000, ram_mib: 8192, disk_mib: 81920 },
];
const valid = value => Number.isSafeInteger(value) && value >= 0;
export const DIMENSIONS = ['cpu_millis', 'ram_mib', 'disk_mib'];
export function hostBudgetGuidance(result) {
  if (result.proposal) return result.proposal;
  const totals = result.totals || {};
  const warnings = [];
  const reserve = {}, budget = {};
  const inventoryKnown = !result.blockers.includes('guest_inventory_unavailable');
  for (const key of DIMENSIONS) {
    const total = totals[key];
    const guestKey = key === 'disk_mib' ? 'root_disk_mib' : key;
    const observed = result.guests.map(guest => guest[guestKey]);
    if (!valid(total) || total === 0 || !inventoryKnown || observed.some(value => !valid(value))) {
      warnings.push(`${key}: usable budget unknown because totals or existing guest limits are incomplete.`);
      continue;
    }
    reserve[key] = Math.min(total, Math.max(key === 'cpu_millis' ? 1000 : key === 'ram_mib' ? 2048 : 10240, Math.ceil(total * 0.1)));
    let guestReserved = observed.reduce((sum, value) => sum + value, 0);
    if (!Number.isSafeInteger(guestReserved)) { warnings.push(`${key}: guest limits exceed the supported planning range.`); continue; }
    if (key === 'disk_mib') {
      // Missing usage is not zero. The backend currently omits both zero and
      // unavailable disk usage, so do not silently recommend disk space.
      if (!valid(totals.disk_mib_used)) { warnings.push('disk_mib: disk usage unknown; enter a reviewed disk budget manually.'); continue; }
      // No per-guest used-bytes attribution exists here. Reserve current
      // usage PLUS full guest limits so host data and future guest growth
      // cannot be mistaken for reusable capacity (overlap is conservative).
      const guestDiskLimits = guestReserved;
      guestReserved += totals.disk_mib_used;
      if (!Number.isSafeInteger(guestReserved)) { warnings.push('disk_mib: disk allowance exceeds the supported planning range.'); continue; }
      if (totals.storage_pool) {
        if (!valid(totals.storage_pool_disk_mib) || !valid(totals.storage_pool_disk_mib_used)) {
          warnings.push('disk_mib: selected storage pool capacity or usage is unknown.'); continue;
        }
        const poolFree = Math.max(0, totals.storage_pool_disk_mib - totals.storage_pool_disk_mib_used - guestDiskLimits - Math.max(10240, Math.ceil(totals.storage_pool_disk_mib * 0.1)));
        budget[key] = Math.min(Math.max(0, total - reserve[key] - guestReserved), poolFree);
        continue;
      }
    }
    budget[key] = Math.max(0, total - reserve[key] - guestReserved);
  }
  if (budget.disk_mib === 0) {
    delete budget.disk_mib;
    warnings.push('No disk capacity remains in this proposal. Disk budget is left blank because zero would mean unconstrained, not no capacity.');
  }
  warnings.push('Existing guest limits are reserved conservatively, including attributed guests. Other projects and host services may be missing; this is a proposal, not qualified free capacity.');
  return { budget, reserve, warnings };
}
export function assessBudget(result, budget, guidance = hostBudgetGuidance(result)) {
  return DIMENSIONS.flatMap(key => {
    const value = Number(budget[key]);
    if (budget[key] === '' || !valid(value)) return [`${key}: enter a non-negative whole number.`];
    if (key === 'disk_mib' && value === 0) return ['Disk budget 0 means unconstrained disk accounting, not zero capacity. Choose an explicit disk limit.'];
    const total = result.totals?.[key];
    if (!valid(total) || total === 0) return [`${key}: physical capacity is unknown; this budget cannot be checked.`];
    if (value > total) return [`${key}: assigned budget exceeds detected physical capacity.`];
    if (key === 'disk_mib' && result.totals.storage_pool && valid(result.totals.storage_pool_disk_mib) &&
        result.totals.storage_pool_disk_mib > 0 && value > result.totals.storage_pool_disk_mib) {
      return [`disk_mib: assigned budget exceeds the detected selected storage pool total of ${result.totals.storage_pool_disk_mib} MiB.`];
    }
    const proposed = guidance.budget[key];
    if (proposed === undefined) return [`${key}: no budget was suggested. Review the discovery warnings before assigning a manual value.`];
    if (value > proposed) {
      if (key === 'disk_mib') {
        const pool = result.totals.storage_pool ? ' and the selected storage pool' : '';
        return [`disk_mib: assigned budget is ${value - proposed} MiB above the suggested ${proposed} MiB after accounting for used space, observed guest limits, system headroom${pool}. Review storage before overriding.`];
      }
      return [`${key}: only ${total - value} remains outside this runner budget. The override is ${value - proposed} above the suggested ${proposed}, leaving less allowance for system headroom and observed guests.`];
    }
    return [];
  });
}
export function runnerMix(budget, profiles, counts) {
  const totals = Object.fromEntries(DIMENSIONS.map(key => [key, 0]));
  if (DIMENSIONS.some(key => !valid(Number(budget[key])) || budget[key] === '' || Number(budget[key]) <= 0)) return { unknown: true };
  for (let index = 0; index < profiles.length; index++) {
    const count = Number(counts[index]);
    if (counts[index] === '' || !valid(count)) return { invalid: true };
    for (const key of DIMENSIONS) {
      if (!valid(profiles[index][key]) || profiles[index][key] === 0) { if (count > 0) return { invalid: true }; continue; }
      totals[key] += count * profiles[index][key];
      if (!valid(totals[key])) return { invalid: true };
    }
  }
  const fits = profiles.map(profile => DIMENSIONS.some(key => !valid(profile[key]) || profile[key] === 0) ? null : Math.min(...DIMENSIONS.map(key => Math.floor(Number(budget[key]) / profile[key]))));
  return { totals, fits, exceeds: DIMENSIONS.filter(key => totals[key] > Number(budget[key])) };
}

// An individual profile's resource ceiling does not depend on proposed mix
// counts. CPU and RAM can be known zero; a zero disk budget is unconstrained.
export function runnerProfileMaximum(budget, profile) {
  if (DIMENSIONS.some(key => !enteredInteger(budget?.[key]) ||
      !valid(profile?.[key]) || profile[key] === 0) || Number(budget.disk_mib) === 0) return null;
  return Math.min(...DIMENSIONS.map(key => Math.floor(Number(budget[key]) / profile[key])));
}

// Presentation-only deadline from the exact discovery review. Older responses
// still support manual enrollment, but cannot copy their plan into the draft.
export function runnerPlanningReviewWindow(result) {
  const review = result?.review;
  if (!review || review.profile_id !== result.profile_id ||
      typeof review.id !== 'string' || !/^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/.test(review.id) ||
      typeof review.revision !== 'string' || !/^[0-9a-f]{64}$/.test(review.revision) ||
      typeof result.observed_at !== 'string' || typeof result.review_expires_at !== 'string') return undefined;
  const observed = Date.parse(result.observed_at), expires = Date.parse(result.review_expires_at);
  if (!Number.isFinite(observed) || !Number.isFinite(expires) || expires <= observed) return undefined;
  return { observed, expires };
}

const enteredInteger = value => typeof value === 'number' ? valid(value) :
  typeof value === 'string' && /^[0-9]+$/.test(value) && valid(Number(value));

// Share the evidence check between the displayed recommendation and the draft
// copy control. A legacy, incomplete or expired proposal is not a reviewed budget.
export function reviewedRunnerBudget(result, now = Date.now()) {
  const window = runnerPlanningReviewWindow(result), proposal = result?.proposal;
  if (!window || !Number.isFinite(now) || now < window.observed || now >= window.expires ||
      proposal?.version !== 1 || !['saved-profiles','illustrative-presets'].includes(proposal.size_source) ||
      !Array.isArray(proposal.sizes) || proposal.sizes.length < 1 || proposal.sizes.length > 512 ||
      DIMENSIONS.some(key => !valid(proposal.budget?.[key])) || proposal.budget.disk_mib === 0) return undefined;
  return proposal.budget;
}

// Do not treat a manual override of unknown/headroom-constrained physical
// dimensions as evidence. A copied count fits BOTH the entered draft and the
// existing reviewed recommendation. This cannot save or activate anything.
export function plannedRunnerLimit(result, budget, counts, now = Date.now()) {
  const reviewed = reviewedRunnerBudget(result, now), proposal = result?.proposal;
  if (!reviewed || !Array.isArray(counts) || counts.length !== proposal.sizes.length ||
      DIMENSIONS.some(key => reviewed[key] === 0 || !enteredInteger(budget?.[key]) || Number(budget[key]) === 0)) return undefined;

  let total = 0;
  for (let i = 0; i < counts.length; i++) {
    if (!enteredInteger(counts[i]) || !proposal.sizes[i]) return undefined;
    total += Number(counts[i]);
    if (!valid(total)) return undefined;
  }
  if (total === 0) return undefined;
  const mix = runnerMix(budget, proposal.sizes, counts);
  if (mix.unknown || mix.invalid || mix.exceeds.length ||
      DIMENSIONS.some(key => mix.totals[key] > reviewed[key])) return undefined;
  return total;
}
