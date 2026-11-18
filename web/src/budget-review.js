export const BUDGET_FIELDS = ['cpu_millis','ram_mib','disk_mib','runners'];
export function readBudget(form) {
  const result = {};
  for (const key of BUDGET_FIELDS) {
    const raw = form.get(key);
    const value = Number(raw);
    if (raw === null || String(raw).trim() === '' || !Number.isSafeInteger(value) || value < 0)
      throw new Error('Every budget dimension must be an explicit non-negative safe integer.');
    result[key] = value;
  }
  return result;
}
export function budgetWarnings(selected, budget, host = false) {
  if (!budget) return [];
  const warnings = [];
  if (budget.disk_mib === 0) warnings.push('Disk budget 0 disables the disk ceiling; it does not mean zero disk capacity.');
  for (const key of BUDGET_FIELDS) {
    const value = budget[key];
    if (!Number.isSafeInteger(value) || value < 0) continue;
    const charged = selected?.allocated?.[key];
    if (!(key === 'disk_mib' && value === 0) && Number.isSafeInteger(charged) && value < charged)
      warnings.push(`${key}: ${charged} remains charged above the proposed ${value} ceiling. Lowering the ceiling does not free resources.`);
    if (!host || key === 'runners') continue;
    const total = selected?.detected?.[key];
    if (!Number.isSafeInteger(total) || total <= 0) { warnings.push(`${key}: detected host capacity is unknown; this budget cannot be checked against it.`); continue; }
    if (key === 'disk_mib' && value === 0) continue;
    const reserve = Math.min(total, Math.max(key === 'cpu_millis' ? 1000 : key === 'ram_mib' ? 2048 : 10240, Math.ceil(total * 0.1)));
    if (value > total) warnings.push(`${key}: the proposed budget exceeds the last detected host total of ${total}.`);
    else if (total - value < reserve) warnings.push(`${key}: only ${total - value} remains outside the runner budget; suggested system headroom is at least ${reserve}. Other host workloads may need more.`);
  }
  if (host) warnings.push('Headroom uses last-discovered totals, not current free capacity. Existing host services and unmanaged workloads may need additional allowance.');
  return warnings;
}
