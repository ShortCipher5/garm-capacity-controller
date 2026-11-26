const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const kinds = new Set(["enterprise", "organization", "repository"]);
// Presentation normalization only: no credentials, enrollment or write grant.
export function entityOnboardingView(value) {
  if (!value || !Array.isArray(value.entities) || value.entities.length > 1000) return { available: false, entities: [] };
  let previous = "", total = 0;
  const entities = [];
  for (const row of value.entities) {
    if (!row || typeof row !== "object" || Array.isArray(row) || typeof row.id !== "string" || !kinds.has(row.type) || !uuid.test(row.id) || row.id === "00000000-0000-0000-0000-000000000000" || !Number.isSafeInteger(row.pool_count) || row.pool_count < 0 || row.pool_count > 1000 ||
        (row.configured !== undefined && (row.configured !== true || row.type !== "repository")) ||
        (row.pool_count === 0 && row.configured !== true)) return { available: false, entities: [] };
    const key = `${row.type}/${row.id}`;
    total += row.pool_count;
    if (key <= previous || total > 1000) return { available: false, entities: [] };
    previous = key;
    entities.push({ type: row.type, id: row.id, pool_count: row.pool_count, ...(row.configured && {configured:true}) });
  }
  return { available: value.available === true, entities };
}
export function existingEntityChoice(value, key) {
  const view = entityOnboardingView(value);
  if (!view.available) return null;
  const row = view.entities.find(row => `${row.type}/${row.id}` === key);
  return row ? { type: row.type, id: row.id } : null;
}
