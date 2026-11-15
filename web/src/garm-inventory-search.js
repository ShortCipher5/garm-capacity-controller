// Local display projection only. Never feed these filtered rows into enrollment,
// missing-object detection, settings review or capacity accounting.
export const inventoryPageSize = 100;
export function selectInventoryPage(rows, {query = "", provider = "", page = 0} = {}) {
 const needle = query.trim().toLocaleLowerCase();
 const matches = rows.filter(row => (!provider || row.provider_name === provider) && (!needle ||
   [row.id, row.name, row.provider_name, row.owner_kind || "unknown", row.owner_id, ...(row.tags || [])]
    .some(value => value !== undefined && String(value).toLocaleLowerCase().includes(needle))));
 const pages = Math.max(1, Math.ceil(matches.length / inventoryPageSize));
 const current = Math.min(Math.max(0, Number.isSafeInteger(page) ? page : 0), pages - 1);
 return {rows: matches.slice(current * inventoryPageSize, (current + 1) * inventoryPageSize), total: rows.length, matched: matches.length, page: current, pages};
}
export function inventoryProviders(rows, selected = "") {
 return [...new Set([...rows.map(row => row.provider_name), ...(selected ? [selected] : [])])].sort();
}
