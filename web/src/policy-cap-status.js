const integer=v=>Number.isSafeInteger(v)&&v>=0;
const time=v=>v===undefined||typeof v==='string'&&Number.isFinite(Date.parse(v));
export function validatePolicyCapStatus(v){if(!v||!['not_started','running','stopped','blocked'].includes(v.state)||!integer(v.policy_revision)||!integer(v.confirmed)||v.confirmed>10||!integer(v.skipped)||v.skipped>1000||!time(v.last_attempt)||!time(v.last_success)||(v.last_request_id!==undefined&&v.last_request_id!==''&&!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(v.last_request_id)))throw new Error('Unsupported policy-cap reconciliation status.');validateCapPoolResults(v.pool_results);return v;}
export const capEventTime=v=>typeof v==='string'&&!v.startsWith('0001-')?new Date(v).toLocaleString():'not recorded';

export const capPoolLabels = Object.freeze({
  not_examined: 'Not examined in this pass',
  confirmed_reduction: 'Original maximum reduction confirmed',
  unconfirmed: 'Reduction request unconfirmed; preserve the writer journal',
  zero_cap_unsupported: 'Skipped: zero cap is unsupported',
  unsupported_cap: 'Skipped: cap is outside the supported range',
  no_downward_change: 'Skipped: saved cap is not below the observed maximum',
  below_idle_minimum: 'Skipped: saved cap is below the unchanged idle minimum',
});
export function validateCapPoolResults(rows) {
  if (rows === undefined) return;
  if (!Array.isArray(rows) || rows.length > 1000) throw new Error('Unsupported pool reconciliation results.');
  const seen = new Set();
  for (const row of rows) {
    if (!row || typeof row !== 'object' || Object.keys(row).length !== 2 ||
        typeof row.pool_id !== 'string' ||
        !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(row.pool_id) ||
        typeof row.outcome !== 'string' || !Object.hasOwn(capPoolLabels, row.outcome) || seen.has(row.pool_id)) {
      throw new Error('Unsupported pool reconciliation results.');
    }
    seen.add(row.pool_id);
  }
}
